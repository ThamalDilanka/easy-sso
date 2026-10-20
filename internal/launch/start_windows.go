//go:build windows

package launch

import "context"

// Start is refused on native Windows: DocumentDB IAM login is for Linux,
// macOS and WSL.
func (System) Start(context.Context, Request) error { return ErrNativeWindows }
