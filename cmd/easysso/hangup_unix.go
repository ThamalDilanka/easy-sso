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
// Under nohup SIGHUP is ignored and easysso keeps running when the window
// closes; it then registers nothing and returns a nil channel.
func watchHangup() (<-chan os.Signal, func()) {
	if signal.Ignored(syscall.SIGHUP) {
		return nil, func() {}
	}
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	return hangup, func() { signal.Stop(hangup) }
}
