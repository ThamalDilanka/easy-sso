//go:build windows

package awscli

import "os/exec"

// Windows keeps the default cancel; the process Job ends the plugin when
// its handle closes after Wait.
func groupCancel(*exec.Cmd) {}
