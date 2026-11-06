//go:build !windows

package daemon

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileLock is an exclusive advisory lock held for the hub's whole life. The OS drops it if the
// process dies, so the lock (not a pid) decides whether a hub is running.
type fileLock struct{ f *os.File }

// tryLock takes the lock without waiting; ok is false if another holder has it.
func tryLock(path string) (l *fileLock, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{f}, true, nil
}

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
