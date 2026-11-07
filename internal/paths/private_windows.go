//go:build windows

package paths

import "os"

// Chmod is a no-op on Windows.
func Chmod(string, os.FileMode) error { return nil }
