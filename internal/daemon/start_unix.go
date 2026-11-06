//go:build !windows

package daemon

import "syscall"

func detachAttr(bool) *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func retryWithoutBreakaway(error) bool { return false }
