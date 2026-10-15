package terminal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

// Embedded owns PTYs and terminal screens. Screen contents stay in memory and
// never enter app metadata, logs, or the AWS credential exchange.
type Embedded struct {
	mu      sync.Mutex
	screens map[string]*embeddedScreen
}
type embeddedScreen struct {
	mu            sync.Mutex
	emu           *vt.Emulator
	pty           io.ReadWriteCloser
	resize        func(int, int) error
	stop          func() error
	width, height int
	done          chan struct{}
	closed        bool
	cursorVisible bool
	input         chan terminalInput
	offset        int                   // lines the viewport sits above live output
	mouse         map[ansi.DECMode]bool // mouse-reporting modes the application enabled
}
type terminalInput struct {
	key, text string
	paste     bool
}

func NewEmbedded() (*Embedded, error) {
	if !embeddedSupported() {
		return nil, errors.New("embedded terminals require Linux, WSL or macOS")
	}
	return &Embedded{screens: map[string]*embeddedScreen{}}, nil
}
func (e *Embedded) Capabilities() Capabilities {
	return Capabilities{LaunchTab: true, FocusExisting: true, StableHandles: true}
}
func (e *Embedded) Focus(id string) error {
	if !e.Has(id) {
		return errors.New("terminal is not embedded in this workspace")
	}
	return nil
}
func (e *Embedded) Has(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.screens[id]
	return ok
}
func (e *Embedded) get(id string) (*embeddedScreen, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.screens[id]
	if s == nil {
		return nil, errors.New("terminal is not embedded in this workspace")
	}
	return s, nil
}
func (e *Embedded) Launch(ctx context.Context, r Request) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r.Executable == "" || r.StateDir == "" || !regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`).MatchString(r.RunID) {
		return "", errors.New("invalid embedded helper request")
	}
	// Connection command cancellation must not destroy a confirmed shell on return.
	cmd := exec.Command(r.Executable, "internal-helper", "--state-dir", r.StateDir, "--run-id", r.RunID)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	s, err := startScreen(cmd, 80, 24)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.screens[r.RunID] = s
	e.mu.Unlock()
	return r.RunID, nil
}

// LaunchClient owns a local interactive client with the same tab/input lifecycle
// as an SSM helper. The filter runs before any output enters the emulator.
func (e *Embedded) LaunchClient(ctx context.Context, id string, cmd *exec.Cmd, filter func([]byte) []byte) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.screens[id]; exists {
		return nil, errors.New("close the existing terminal tab before reconnecting")
	}
	s, err := startScreen(cmd, 80, 24, filter)
	if err != nil {
		return nil, errors.New("could not start the PostgreSQL terminal")
	}
	e.screens[id] = s
	return s.done, nil
}

func startScreen(cmd *exec.Cmd, width, height int, filters ...func([]byte) []byte) (*embeddedScreen, error) {
	master, resize, stop, err := startPTY(cmd, width, height)
	if err != nil {
		return nil, err
	}
	cmd.Env = nil // the child has inherited its environment; release our copy
	s := &embeddedScreen{emu: vt.NewEmulator(width, height), pty: master, resize: resize, stop: stop, width: width, height: height, done: make(chan struct{}), input: make(chan terminalInput, 128)}
	s.cursorVisible = true
	s.mouse = map[ansi.DECMode]bool{}
	// Callbacks run inside emu.Write while the output goroutine holds s.mu.
	s.emu.SetCallbacks(vt.Callbacks{
		CursorVisibility: func(visible bool) { s.cursorVisible = visible },
		AltScreen:        func(bool) { s.offset = 0 },
		EnableMode: func(m ansi.Mode) {
			if mode, ok := mouseMode(m); ok {
				s.mouse[mode] = true
			}
		},
		DisableMode: func(m ansi.Mode) {
			if mode, ok := mouseMode(m); ok {
				delete(s.mouse, mode)
			}
		},
	})
	s.emu.SetScrollbackSize(5000)
	// Emulator replies (cursor reports, terminal queries, key encoding) go only to
	// this child's PTY, never to the host terminal.
	inputDone := make(chan struct{})
	go func() { defer close(inputDone); _, _ = io.Copy(master, s.emu) }()
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				output := buf[:n]
				if len(filters) > 0 && filters[0] != nil {
					output = filters[0](output)
				}
				s.mu.Lock()
				_, _ = s.emu.Write(output)
				s.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		_ = cmd.Wait()
		_ = master.Close()
		s.mu.Lock()
		s.closed = true
		// Close the pipe first: vt.Close mutates state also read by Read.
		_ = s.emu.InputPipe().(io.Closer).Close()
		s.mu.Unlock()
		<-inputDone
		s.mu.Lock()
		_ = s.emu.Close()
		s.mu.Unlock()
		close(s.done)
	}()
	go func() {
		for {
			select {
			case input := <-s.input:
				s.mu.Lock()
				if !s.closed {
					sendTerminalKey(s.emu, input)
				}
				s.mu.Unlock()
			case <-s.done:
				return
			}
		}
	}()
	return s, nil
}
func (e *Embedded) Frame(id string, width, height int, focused bool) (string, error) {
	s, err := e.get(id)
	if err != nil {
		return "", err
	}
	width, height = max(1, width), max(1, height)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.width != width || s.height != height {
		if !s.closed {
			if err = s.resize(width, height); err != nil {
				return "", err
			}
		}
		s.emu.Resize(width, height)
		s.width, s.height = width, height
	}
	// Render only emulator cells. No raw remote escape sequence reaches the UI.
	if s.offset > 0 && !s.emu.IsAltScreen() {
		return s.history(height), nil
	}
	if focused && !s.closed && s.cursorVisible {
		pos := s.emu.CursorPosition()
		original := s.emu.CellAt(pos.X, pos.Y)
		if original != nil {
			saved := *original
			original = &saved
		}
		cursor := uv.Cell{Content: " ", Width: 1}
		if original != nil {
			cursor = *original
		}
		cursor.Style.Attrs ^= uv.AttrReverse
		s.emu.SetCell(pos.X, pos.Y, &cursor)
		frame := s.emu.Render()
		s.emu.SetCell(pos.X, pos.Y, original)
		return frame, nil
	}
	return s.emu.Render(), nil
}

// history renders the viewport while scrolled: retained lines first, then live
// rows. Both come from the emulator, so no raw output reaches the UI.
func (s *embeddedScreen) history(height int) string {
	sb := s.emu.Scrollback()
	n := sb.Len()
	s.offset = min(s.offset, n)
	rows := make([]string, 0, height)
	for i := n - s.offset; i < n && len(rows) < height; i++ {
		rows = append(rows, sb.Line(i).Render())
	}
	live := strings.Split(s.emu.Render(), "\n")
	for i := 0; i < len(live) && len(rows) < height; i++ {
		rows = append(rows, live[i])
	}
	return strings.Join(rows, "\n")
}
func (s *embeddedScreen) scroll(lines int) {
	s.offset = max(0, min(s.offset+lines, s.emu.ScrollbackLen()))
}
func mouseMode(m ansi.Mode) (ansi.DECMode, bool) {
	mode, ok := m.(ansi.DECMode)
	if !ok {
		return 0, false
	}
	switch mode {
	case ansi.ModeMouseX10, ansi.ModeMouseNormal, ansi.ModeMouseHighlight, ansi.ModeMouseButtonEvent, ansi.ModeMouseAnyEvent:
		return mode, true
	}
	return 0, false
}

// Scroll moves the viewport over retained output: lines > 0 shows older
// output, lines < 0 newer. A full-screen application owns the alternate
// screen, so there the gesture becomes a PgUp or PgDn key for it instead.
func (e *Embedded) Scroll(id string, lines int) error {
	s, err := e.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if lines == 0 {
		return nil
	}
	if !s.emu.IsAltScreen() {
		s.scroll(lines)
		return nil
	}
	if !s.closed {
		key := "pgup"
		if lines < 0 {
			key = "pgdown"
		}
		sendTerminalKey(s.emu, terminalInput{key: key})
	}
	return nil
}

// Wheel handles one mouse wheel notch over cell (x, y). On the primary screen
// it scrolls the viewport by three lines. On the alternate screen it is
// reported to the application as a wheel event when the application enabled
// mouse reporting, otherwise as three arrow keys.
func (e *Embedded) Wheel(id string, up bool, x, y int) error {
	s, err := e.get(id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.emu.IsAltScreen() {
		if up {
			s.scroll(3)
		} else {
			s.scroll(-3)
		}
		return nil
	}
	if s.closed {
		return nil
	}
	if len(s.mouse) > 0 {
		button := uv.MouseWheelDown
		if up {
			button = uv.MouseWheelUp
		}
		s.emu.SendMouse(uv.MouseWheelEvent{Button: button, X: x, Y: y})
		return nil
	}
	key := "down"
	if up {
		key = "up"
	}
	for i := 0; i < 3; i++ {
		sendTerminalKey(s.emu, terminalInput{key: key})
	}
	return nil
}

// ScrollPosition reports how far the viewport sits above live output and how
// many lines are retained; both are zero on the alternate screen.
func (e *Embedded) ScrollPosition(id string) (offset, history int) {
	s, err := e.get(id)
	if err != nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.emu.IsAltScreen() {
		return 0, 0
	}
	return s.offset, s.emu.ScrollbackLen()
}

// Send delivers input and returns the viewport to live output first, so the
// user always sees the shell react to what they typed.
func (e *Embedded) Send(id, key, text string, paste bool) error {
	s, err := e.get(id)
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return errors.New("terminal has ended")
	default:
	}
	s.mu.Lock()
	s.offset = 0
	s.mu.Unlock()
	select {
	case s.input <- terminalInput{key, text, paste}:
		return nil
	default:
		return errors.New("terminal input is busy; try again")
	}
}

// Exited is based on the owned process completion signal, never a saved PID.
func (e *Embedded) Exited(id string) bool {
	s, err := e.get(id)
	if err != nil {
		return false
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (e *Embedded) Close(id string) error {
	s, err := e.get(id)
	if err != nil {
		return nil
	}
	select {
	case <-s.done:
	default:
		_ = s.pty.Close()
		err = s.stop()
		if err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			return errors.New("local terminal cleanup timed out")
		}
	}
	e.mu.Lock()
	if e.screens[id] == s {
		delete(e.screens, id)
	}
	e.mu.Unlock()
	return nil
}

func (e *Embedded) CloseAll() {
	e.mu.Lock()
	ids := make([]string, 0, len(e.screens))
	for id := range e.screens {
		ids = append(ids, id)
	}
	e.mu.Unlock()
	for _, id := range ids {
		_ = e.Close(id)
	}
}

func sendTerminalKey(emu *vt.Emulator, input terminalInput) {
	if input.paste {
		emu.Paste(input.text)
		return
	}
	if input.text != "" && !strings.HasPrefix(input.key, "alt+") {
		for _, r := range input.text {
			emu.SendKey(uv.KeyPressEvent{Code: r})
		}
		return
	}
	key := input.key
	mods := uv.KeyMod(0)
	for {
		switch {
		case strings.HasPrefix(key, "ctrl+"):
			mods |= uv.ModCtrl
			key = strings.TrimPrefix(key, "ctrl+")
		case strings.HasPrefix(key, "alt+"):
			mods |= uv.ModAlt
			key = strings.TrimPrefix(key, "alt+")
		case strings.HasPrefix(key, "shift+"):
			mods |= uv.ModShift
			key = strings.TrimPrefix(key, "shift+")
		default:
			goto mapped
		}
	}
mapped:
	codes := map[string]rune{"enter": uv.KeyEnter, "esc": uv.KeyEscape, "tab": uv.KeyTab, "backspace": uv.KeyBackspace, "delete": uv.KeyDelete, "up": uv.KeyUp, "down": uv.KeyDown, "left": uv.KeyLeft, "right": uv.KeyRight, "home": uv.KeyHome, "end": uv.KeyEnd, "pgup": uv.KeyPgUp, "pgdown": uv.KeyPgDown, " ": uv.KeySpace, "space": uv.KeySpace}
	code, ok := codes[key]
	if !ok {
		runes := []rune(key)
		if len(runes) == 1 {
			code = runes[0]
		} else {
			for i := 1; i <= 12; i++ {
				if key == fmt.Sprintf("f%d", i) {
					code = uv.KeyF1 + rune(i-1)
				}
			}
		}
	}
	if code != 0 {
		emu.SendKey(uv.KeyPressEvent{Code: code, Mod: mods})
	}
}
