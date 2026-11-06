//go:build windows

package hub

import (
	"os"

	"golang.org/x/sys/windows"
)

// fileLock is an exclusive lock held for the hub's whole life. Windows drops it if the process
// dies, so the lock (not a pid) decides whether a hub is running.
type fileLock struct{ f *os.File }

// tryLock takes the lock without waiting; ok is false if another holder has it.
func tryLock(path string) (l *fileLock, ok bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, false, err
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_IO_PENDING {
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
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(l.f.Fd()), 0, 1, 0, ol)
	_ = l.f.Close()
	l.f = nil
}
