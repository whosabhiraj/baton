//go:build windows

package daemon

import (
	"errors"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	createBreakawayJob    = 0x01000000
)

// detachAttr: CREATE_NO_WINDOW gives the hub a hidden console its children inherit (no popup
// windows); CREATE_BREAKAWAY_FROM_JOB keeps it alive when a kill-on-close job ends the terminal.
func detachAttr(breakaway bool) *syscall.SysProcAttr {
	f := uint32(createNewProcessGroup | createNoWindow)
	if breakaway {
		f |= createBreakawayJob
	}
	return &syscall.SysProcAttr{CreationFlags: f, HideWindow: true}
}

// retryWithoutBreakaway: the job forbids breakaway -> CreateProcess fails with access denied.
func retryWithoutBreakaway(err error) bool {
	var en syscall.Errno
	return errors.As(err, &en) && en == syscall.ERROR_ACCESS_DENIED
}
