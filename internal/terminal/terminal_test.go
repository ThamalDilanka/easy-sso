package terminal

import (
	"context"
	"strings"
	"testing"
)

func TestStructuredNativeArgumentsAndHonestFocus(t *testing.T) {
	r := Request{Executable: "/path with spaces/easysso", StateDir: "/private path/state", RunID: "01234567-89ab-4def-0123-456789abcdef", Title: "api\x1b\n · demo"}
	for _, kind := range []string{"gnome", "windows", "wsl", "macos"} {
		t.Run(kind, func(t *testing.T) {
			n := &Native{Kind: kind, Distro: "Ubuntu-work", User: "developer"}
			a, e := n.Arguments(r)
			if e != nil {
				t.Fatal(e)
			}
			found := false
			for _, v := range a {
				if v == r.Executable {
					found = true
				}
				if strings.Contains(v, "api\x1b") {
					t.Fatal("unsafe title")
				}
			}
			if !found {
				t.Fatal("executable path was shell-concatenated")
			}
			if n.Capabilities().FocusExisting || n.Focus(r.RunID) == nil {
				t.Fatal("unqualified stable focus")
			}
			if kind == "windows" || kind == "wsl" {
				if len(a) < 3 || a[0] != "-w" || a[1] != "new" || a[2] != "new-tab" {
					t.Fatal("must request a separate window, not an existing host window")
				}
			}
			if kind == "gnome" && a[0] != "--window" {
				t.Fatal("must request a GNOME window explicitly")
			}
			if n.Capabilities().LaunchTab {
				t.Fatal("native launcher only creates windows")
			}
			if kind == "wsl" && !strings.Contains(strings.Join(a, " "), "--distribution Ubuntu-work --user developer --exec") {
				t.Fatal("WSL identity not pinned")
			}
		})
	}
}
func TestInvalidLaunchIdentifiersAndWSLUnpinned(t *testing.T) {
	for _, r := range []Request{{Executable: "/bin/a", StateDir: "/tmp", RunID: "bad;command"}, {Executable: "", StateDir: "/tmp", RunID: strings.Repeat("a", 36)}} {
		if _, e := (&Native{Kind: "gnome"}).Arguments(r); e == nil {
			t.Fatal("invalid launch accepted")
		}
	}
	n := &Native{Kind: "wsl"}
	if _, e := n.Arguments(Request{Executable: "/bin/a", StateDir: "/tmp", RunID: strings.Repeat("a", 36)}); e == nil {
		t.Fatal("unpinned WSL accepted")
	}
	if _, e := n.Launch(context.Background(), Request{}); e == nil {
		t.Fatal("invalid launch accepted")
	}
}
