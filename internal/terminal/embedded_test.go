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
