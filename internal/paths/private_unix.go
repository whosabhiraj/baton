//go:build !windows

package paths

import "os"

// Chmod tightens permissions (no-op on Windows).
func Chmod(p string, m os.FileMode) error { return os.Chmod(p, m) }
