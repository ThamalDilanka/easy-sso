//go:build !windows

package terminal

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func testScreen(t *testing.T, id string, e *Embedded) *embeddedScreen {
	t.Helper()
	cmd := exec.Command("bash", "--noprofile", "--norc")
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "HISTFILE=/dev/null", "TERM=xterm-256color", "PS1=READY> ")
	screen, err := startScreen(cmd, 80, 20)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.screens[id] = screen
	e.mu.Unlock()
	t.Cleanup(func() {
		_ = e.Close(id)
		select {
		case <-screen.done:
		case <-time.After(3 * time.Second):
			t.Error("terminal child did not exit")
		}
	})
	waitFrame(t, e, id, "READY>", 80, 20)
	return screen
}
func waitFrame(t *testing.T, e *Embedded, id, want string, w, h int) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var frame string
	for time.Now().Before(deadline) {
		var err error
		frame, err = e.Frame(id, w, h, false)
		if err != nil {
			t.Fatal(err)
		}
		frame = ansi.Strip(frame)
		if strings.Contains(frame, want) {
			return frame
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("missing %q in %q", want, frame)
	return ""
}
func typeText(t *testing.T, e *Embedded, id, text string) {
	t.Helper()
	if err := e.Send(id, "", text, false); err != nil {
		t.Fatal(err)
	}
}
func TestEmbeddedPTYInputResizeAndIndependentScreens(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	testScreen(t, "one", e)
	testScreen(t, "two", e)
	typeText(t, e, "one", "stty -echo; printf 'RESULT:%s\\n' $((6*7))\n")
	waitFrame(t, e, "one", "RESULT:42", 80, 20)
	typeText(t, e, "one", "printf 'PARTIAL:%s\\n' ")
	typeText(t, e, "two", "printf 'OTHER:%s\\n' $((9*9))\n")
	waitFrame(t, e, "two", "OTHER:81", 80, 20)
	typeText(t, e, "one", "$((4*4))\n")
	frame := waitFrame(t, e, "one", "PARTIAL:16", 80, 20)
	if strings.Contains(frame, "OTHER:81") {
		t.Fatal("server screens mixed")
	}
	_, err = e.Frame("one", 91, 17, true)
	if err != nil {
		t.Fatal(err)
	}
	typeText(t, e, "one", "stty size\n")
	waitFrame(t, e, "one", "17 91", 91, 17)
	typeText(t, e, "one", "printf '\\033[2J\\033[HCLEARED'\n")
	waitFrame(t, e, "one", "CLEARED", 91, 17)
}

func TestCloseRemovesOwnedScreenAndStopsChild(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	screen := testScreen(t, "close", e)
	if err := e.Close("close"); err != nil {
		t.Fatal(err)
	}
	if e.Has("close") {
		t.Fatal("closed shell can still be reopened")
	}
	select {
	case <-screen.done:
	default:
		t.Fatal("closed screen before its child exited")
	}
	if err := e.Close("close"); err != nil {
		t.Fatal("repeat close", err)
	}
}

func TestNaturallyExitedScreenCanBeClosed(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	screen := testScreen(t, "exit", e)
	typeText(t, e, "exit", "exit\n")
	select {
	case <-screen.done:
	case <-time.After(3 * time.Second):
		t.Fatal("shell did not exit")
	}
	if !e.Exited("exit") {
		t.Fatal("owned completion not reported")
	}
	if err := e.Close("exit"); err != nil || e.Has("exit") {
		t.Fatal("could not dismiss exited screen", err)
	}
}

func TestScrollShowsRetainedOutputAndInputSnapsBack(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	testScreen(t, "sb", e)
	typeText(t, e, "sb", "seq 1 200\n")
	waitFrame(t, e, "sb", "\n200\n", 80, 20)
	if err := e.Scroll("sb", 5000); err != nil {
		t.Fatal(err)
	}
	frame, _ := e.Frame("sb", 80, 20, true)
	plain := ansi.Strip(frame)
	if !strings.HasPrefix(plain, "READY> seq 1 200\n1\n2\n") {
		t.Fatalf("oldest history not shown: %q", plain)
	}
	if strings.Contains(frame, "\x1b[7m") {
		t.Fatal("cursor drawn while scrolled")
	}
	offset, history := e.ScrollPosition("sb")
	if offset != history || history < 180 {
		t.Fatalf("position %d/%d", offset, history)
	}
	if err := e.Scroll("sb", -history+1); err != nil {
		t.Fatal(err)
	}
	if offset, _ = e.ScrollPosition("sb"); offset != 1 {
		t.Fatalf("offset after partial scroll: %d", offset)
	}
	typeText(t, e, "sb", "")
	if offset, _ = e.ScrollPosition("sb"); offset != 0 {
		t.Fatal("input did not snap back to live output")
	}
	frame = waitFrame(t, e, "sb", "182\n183", 80, 20)
	if !strings.HasPrefix(frame, "182\n") {
		t.Fatalf("live frame not restored: %q", frame)
	}
}

func TestScrollClampsAndStaysWhileOutputArrives(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	testScreen(t, "clamp", e)
	if err := e.Scroll("clamp", 50); err != nil {
		t.Fatal(err)
	}
	if offset, _ := e.ScrollPosition("clamp"); offset != 0 {
		t.Fatalf("scrolled past empty history: %d", offset)
	}
	typeText(t, e, "clamp", "seq 1 60\n")
	waitFrame(t, e, "clamp", "\n60\n", 80, 20)
	if err := e.Scroll("clamp", 10); err != nil {
		t.Fatal(err)
	}
	if err := e.Wheel("clamp", true, 0, 0); err != nil {
		t.Fatal(err)
	}
	if offset, _ := e.ScrollPosition("clamp"); offset != 13 {
		t.Fatalf("wheel did not add three lines: %d", offset)
	}
	typeText(t, e, "clamp", "(sleep 0.3; seq 61 90) &\n")
	waitFrame(t, e, "clamp", "seq 61 90", 80, 20)
	if offset, _ := e.ScrollPosition("clamp"); offset != 0 {
		t.Fatal("typing did not return to live output")
	}
	if err := e.Scroll("clamp", 10); err != nil {
		t.Fatal(err)
	}
	// Output arriving while scrolled grows history underneath a fixed viewport.
	_, before := e.ScrollPosition("clamp")
	deadline := time.Now().Add(3 * time.Second)
	for {
		offset, history := e.ScrollPosition("clamp")
		if history >= before+30 {
			if offset != 10 {
				t.Fatalf("new output moved the viewport: offset %d", offset)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background output never arrived: history %d", history)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.Frame("clamp", 40, 10, false); err != nil {
		t.Fatal(err)
	}
	if offset, _ := e.ScrollPosition("clamp"); offset != 10 {
		t.Fatal("resize changed the viewport offset")
	}
}

func TestAlternateScreenGesturesGoToApplication(t *testing.T) {
	e, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	testScreen(t, "alt", e)
	typeText(t, e, "alt", "seq 1 100 | less\n")
	waitFrame(t, e, "alt", "\n19\n", 80, 20)
	if err := e.Wheel("alt", false, 0, 0); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, e, "alt", "4\n5\n6", 80, 20)
	if offset, _ := e.ScrollPosition("alt"); offset != 0 {
		t.Fatal("local viewport moved on the alternate screen")
	}
	if err := e.Scroll("alt", -19); err != nil {
		t.Fatal(err)
	}
	waitFrame(t, e, "alt", "\n40\n", 80, 20)
	typeText(t, e, "alt", "q")
	waitFrame(t, e, "alt", "READY> seq 1 100 | less", 80, 20)
	typeText(t, e, "alt", "seq 1 100 | less --mouse\n")
	waitFrame(t, e, "alt", "\n19\n", 80, 20)
	if err := e.Wheel("alt", false, 10, 5); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		frame, err := e.Frame("alt", 80, 20, false)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(ansi.Strip(frame), "1\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("wheel report did not reach less: %q", ansi.Strip(frame))
		}
		time.Sleep(10 * time.Millisecond)
	}
	typeText(t, e, "alt", "q")
	waitFrame(t, e, "alt", "READY> seq 1 100 | less --mouse", 80, 20)
}
