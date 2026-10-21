//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// watchHangup reports SIGHUP, which closing the terminal window sends. Without
// it the default action exits at once and skips the shutdown that ends tunnels.
// Call stop only when run returns, so a late SIGHUP cannot cut cleanup short.
func watchHangup() (<-chan os.Signal, func()) {
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	return hangup, func() { signal.Stop(hangup) }
}
