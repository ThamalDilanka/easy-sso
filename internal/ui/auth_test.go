package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestLoginSelectedAutomaticallyCleansUp(t *testing.T) {
	f := setupUI(t)
	selected, _ := f.m.profile()
	key(f.m, "ctrl+a")
	if f.creds.activates != 1 || len(f.creds.selections) != 1 || f.creds.selections[0].Profile.Name != selected.Name {
		t.Fatal("login did not activate exactly the selected profile")
	}
	if f.m.form != nil || f.m.review != nil || f.m.picker {
		t.Fatal("simple login left an extra review")
	}
}

func TestLoginURLSurvivesChunkedInstructionsAndClears(t *testing.T) {
	f := setupUI(t)
	m := f.m
	m.busy = true
	m.receiveAuthNotice("Open this URL:\nhttps://device.example/")
	m.receiveAuthNotice("authorize?user_code=ABCD\nThen enter code ABCD.\n")
	want := "https://device.example/authorize?user_code=ABCD"
	if m.authURL != want {
		t.Fatalf("URL %q", m.authURL)
	}
	if !strings.Contains(m.content(), "Then enter code ABCD.") {
		t.Fatal("instructions lost")
	}
	if press(m, "c") == nil || press(m, "o") == nil {
		t.Fatal("busy state blocked sign-in actions")
	}
	m.Update(authActionMsg{request: m.request, url: want, feedback: "Copied sign-in URL to clipboard."})
	if !strings.Contains(m.content(), "Copied sign-in URL") {
		t.Fatal("copy confirmation absent")
	}
	m.Update(resultMsg{request: m.request, epoch: m.epoch, action: "Activate roles"})
	if m.authURL != "" || m.authNotice != "" || m.authFeedback != "" {
		t.Fatal("transient sign-in data retained")
	}
}

func TestWSLCopyURLUsesWindowsClipboardAndReportsFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	f := setupUI(t)
	m := f.m
	m.services.CopyText = nil // exercise the real WSL writer against a fake clip.exe
	m.busy = true
	m.authURL = "https://device.example/?user_code=ABCD"
	dir := t.TempDir()
	output := filepath.Join(dir, "clipboard")
	t.Setenv("WSL_DISTRO_NAME", "Ubuntu")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COPY_OUTPUT", output)
	clip := filepath.Join(dir, "clip.exe")
	if err := os.WriteFile(clip, []byte("#!/bin/sh\ncat > \"$COPY_OUTPUT\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	m.Update(cmd())
	data, err := os.ReadFile(output)
	if err != nil || len(data) != 2*len(m.authURL) {
		t.Fatal("clipboard did not receive UTF-16 URL", err)
	}
	for i, b := range []byte(m.authURL) {
		if data[2*i] != b || data[2*i+1] != 0 {
			t.Fatal("incorrect clipboard content")
		}
	}
	if !strings.Contains(m.authFeedback, "Copied") {
		t.Fatal("missing success feedback")
	}
	if err := os.WriteFile(clip, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	m.Update(m.authAction("c")())
	if !strings.Contains(m.authFeedback, "Could not copy") {
		t.Fatal("failed copy reported success")
	}
}
