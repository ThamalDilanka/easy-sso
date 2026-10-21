//go:build windows

package main

import "os"

// Windows has no SIGHUP; closing the console keeps today's behaviour.
func watchHangup() (<-chan os.Signal, func()) { return nil, func() {} }
