//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// Closing the terminal window sends SIGHUP. It must reach the quit path
// instead of the default action, which exits before any cleanup.
func TestHangupIsReportedNotFatal(t *testing.T) {
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
