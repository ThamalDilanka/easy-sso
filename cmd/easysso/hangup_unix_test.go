//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// Closing the terminal window sends SIGHUP. It must reach the quit path
// instead of the default action, which exits before any cleanup.
func TestHangupIsReportedNotFatal(t *testing.T) {
	if signal.Ignored(syscall.SIGHUP) {
		t.Skip("SIGHUP ignored (started under nohup)")
	}
	hangup, stop := watchHangup()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hangup:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGHUP was not reported")
	}
}

// Under nohup SIGHUP is ignored: closing the window must not end easysso,
// so watchHangup must not register and turn the hangup into a quit.
func TestHangupIgnoredUnderNohup(t *testing.T) {
	wasIgnored := signal.Ignored(syscall.SIGHUP)
	signal.Ignore(syscall.SIGHUP)
	if !wasIgnored {
		defer signal.Reset(syscall.SIGHUP)
	}
	hangup, stop := watchHangup()
	defer stop()
	if hangup != nil {
		t.Fatal("watchHangup registered for an ignored SIGHUP")
	}
	if !signal.Ignored(syscall.SIGHUP) {
		t.Fatal("SIGHUP no longer ignored")
	}
}
