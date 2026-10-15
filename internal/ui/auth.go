package ui

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

type authActionMsg struct {
	request       uint64
	url, feedback string
}

func (m *Model) receiveAuthNotice(text string) {
	// Bound transient instructions; never save authorization URLs or codes.
	if len(m.authNotice)+len(text) <= 16384 {
		m.authNotice += strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' {
				return r
			}
			if r < 32 || r == 127 || (r >= 0x80 && r <= 0x9f) {
				return -1
			}
			return r
		}, text)
	}
	m.status = text
	matches := regexp.MustCompile(`https://[^\s<>"\x00-\x1f]+`).FindAllString(m.authNotice, -1)
	for _, candidate := range matches {
		candidate = strings.TrimRight(candidate, ").,;")
		u, err := url.Parse(candidate)
		if err == nil && u.Host != "" && u.User == nil {
			if candidate != m.authURL {
				m.authFeedback = ""
			}
			m.authURL = candidate
		}
	}
}

// writeClipboard puts text on the system clipboard. Under WSL the Windows
// clip.exe is used so the text lands on the host clipboard.
func writeClipboard(ctx context.Context, text string) error {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		// Windows clip.exe consumes UTF-16LE reliably, independent of its code page.
		var data bytes.Buffer
		for _, unit := range utf16.Encode([]rune(text)) {
			_ = binary.Write(&data, binary.LittleEndian, unit)
		}
		cmd := exec.CommandContext(ctx, "clip.exe")
		cmd.Stdin = &data
		return cmd.Run()
	}
	return clipboard.WriteAll(text)
}

// copyText returns the clipboard writer in use: the injected one or the default.
func (m *Model) copyText() func(context.Context, string) error {
	if custom := m.services.CopyText; custom != nil {
		return func(_ context.Context, text string) error { return custom(text) }
	}
	return writeClipboard
}

func (m *Model) authAction(key string) tea.Cmd {
	target, request := m.authURL, m.request
	copy := m.copyText()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		var err error
		feedback := "Opened sign-in link in your browser."
		wsl := os.Getenv("WSL_DISTRO_NAME") != ""
		if key == "c" {
			feedback = "Copied sign-in URL to clipboard."
			err = copy(ctx, target)
		} else {
			switch {
			case wsl || runtime.GOOS == "windows":
				err = exec.CommandContext(ctx, "rundll32.exe", "url.dll,FileProtocolHandler", target).Run()
			case runtime.GOOS == "darwin":
				err = exec.CommandContext(ctx, "open", target).Run()
			default:
				err = exec.CommandContext(ctx, "xdg-open", target).Run()
			}
		}
		if err != nil {
			feedback = "Could not open browser. Press c to copy the sign-in URL."
			if key == "c" {
				feedback = "Could not copy URL. Select and copy the displayed link manually."
			}
		}
		return authActionMsg{request: request, url: target, feedback: feedback}
	}
}
