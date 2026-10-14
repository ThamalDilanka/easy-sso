package ui

import (
	"fmt"
	"github.com/ThamalDilanka/easy-sso/internal/domain"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestLongAccountListScrollIndicatorFitsItsPanel(t *testing.T) {
	f := setupUI(t)
	for i := 0; i < 40; i++ {
		f.m.services.Profiles = append(f.m.services.Profiles, domain.Profile{Name: fmt.Sprintf("profile-%02d", i), SSOSession: "source", AccountID: "111111111111"})
	}
	f.m.selectedProfile = len(f.m.services.Profiles) - 1
	for _, width := range []int{100, 120, 140} {
		f.m.width, f.m.height = width, 24
		view := f.m.View()
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatal("scroll indicator broke the frame", line)
			}
		}
		if !strings.Contains(view, "profile-39") {
			t.Fatal("selected profile scrolled out of view")
		}
	}
}

func TestPanelsRemainVisibleWithoutColor(t *testing.T) {
	f := setupUI(t)
	f.m.noColor = true
	for _, ascii := range []bool{false, true} {
		for _, width := range []int{80, 100, 140} {
			for _, height := range []int{24, 35} {
				f.m.ascii, f.m.width, f.m.height = ascii, width, height
				view := f.m.View()
				border := "─"
				if ascii {
					border = "-"
				}
				if !strings.Contains(view, border) || !strings.Contains(view, "[1 EC2]") || !strings.Contains(view, "111111111111") {
					t.Fatal("panel/navigation/identity missing", view)
				}
				if width >= 100 && !strings.Contains(view, "Accounts") {
					t.Fatal("wide account panel missing")
				}
				if strings.Contains(view, "\x1b") {
					t.Fatal("NO_COLOR was ignored")
				}
				lines := strings.Split(view, "\n")
				if len(lines) > height {
					t.Fatalf("height overflow: %d > %d", len(lines), height)
				}
				for _, line := range lines {
					if lipgloss.Width(line) > width {
						t.Fatal("panel width overflow", line)
					}
				}
			}
		}
	}
}
func TestLightAndDarkChromeActuallyRenderColor(t *testing.T) {
	profile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(profile)
	f := setupUI(t)
	f.m.noColor = false
	f.m.dark = true
	dark := f.m.View()
	f.m.dark = false
	light := f.m.View()
	if !strings.Contains(dark, "\x1b[") || !strings.Contains(light, "\x1b[") || dark == light {
		t.Fatal("themes did not produce distinct ANSI colors")
	}
	for _, view := range []string{dark, light} {
		for _, line := range strings.Split(view, "\n") {
			if lipgloss.Width(line) > f.m.width {
				t.Fatal("ANSI styling changed visible panel width")
			}
		}
	}
}
