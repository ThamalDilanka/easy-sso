package ui

import (
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestUnselectedTextRemainsNeutralInLimitedColorTerminals(t *testing.T) {
	previous := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previous)
	for _, profile := range []termenv.Profile{termenv.ANSI256, termenv.ANSI} {
		lipgloss.SetColorProfile(profile)
		for _, dark := range []bool{false, true} {
			f := setupUI(t)
			f.m.noColor = false
			f.m.dark = dark
			for _, color := range []string{f.m.palette().text, f.m.palette().muted} {
				rendered := f.m.paint("Unselected text", color, f.m.palette().background, false)
				pattern := `^\x1b\[(\d+);`
				if profile == termenv.ANSI256 {
					pattern = `^\x1b\[38;5;(\d+);`
				}
				match := regexp.MustCompile(pattern).FindStringSubmatch(rendered)
				if len(match) != 2 {
					t.Fatalf("missing explicit text color: %q", rendered)
				}
				code, _ := strconv.Atoi(match[1])
				neutral := code == 30 || code == 37 || code == 90 || code == 97
				if profile == termenv.ANSI256 {
					neutral = code == 0 || code == 7 || code == 8 || code == 15 || code == 16 || code == 231 || code >= 232 && code <= 255
				}
				if !neutral {
					t.Errorf("profile=%v dark=%v: unselected text has chromatic color %d: %q", profile, dark, code, rendered)
				}
			}
		}
	}
}

func TestWarningRestoresColorsAfterProfileHighlight(t *testing.T) {
	previous := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(previous)
	for _, profile := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
		lipgloss.SetColorProfile(profile)
		for _, dark := range []bool{false, true} {
			for _, width := range []int{100, 140} {
				f := setupUI(t)
				m := f.m
				m.noColor, m.ascii, m.dark, m.width = false, false, dark, width
				m.services.Profiles[m.selectedProfile].Name = "maintenance"
				m.status = "Warning: Selected account 111111111111 · profile [maintenance]. Log in with Ctrl+A before connecting."
				view := m.View()
				screen := vt.NewEmulator(width+1, m.height+1)
				_, err := screen.Write([]byte(strings.ReplaceAll(view, "\n", "\r\n")))
				if err != nil {
					t.Fatal(err)
				}
				reference := vt.NewEmulator(10, 2)
				p := m.palette()
				reference.Write([]byte(m.paint("W", p.orange, p.background, false) + m.paint("P", p.blue, p.selection, true)))
				warningY := -1
				for y, line := range strings.Split(view, "\n") {
					if strings.Contains(ansi.Strip(line), "Warning:") {
						warningY = y
						break
					}
				}
				for _, token := range []string{"Warning:", "Ctrl+A", "[maintenance]"} {
					found := false
					for y, line := range strings.Split(view, "\n") {
						if y < warningY {
							continue
						}
						plain := ansi.Strip(line)
						if at := strings.Index(plain, token); at >= 0 {
							found = true
							x := lipgloss.Width(plain[:at])
							cell := screen.CellAt(x, y)
							ref := reference.CellAt(0, 0)
							if token == "[maintenance]" {
								ref = reference.CellAt(1, 0)
							}
							if !reflect.DeepEqual(cell.Style.Fg, ref.Style.Fg) || !reflect.DeepEqual(cell.Style.Bg, ref.Style.Bg) {
								t.Errorf("profile=%v dark=%v width=%d token=%s: foreground/background mismatch: got %v/%v, want %v/%v", profile, dark, width, token, cell.Style.Fg, cell.Style.Bg, ref.Style.Fg, ref.Style.Bg)
							}
						}
					}
					if !found {
						t.Errorf("missing warning token %s", token)
					}
				}
				screen.Close()
				reference.Close()
			}
		}
	}
}
