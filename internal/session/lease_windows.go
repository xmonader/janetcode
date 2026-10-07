//go:build windows

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// The lock covers one byte far past the lease record, so a reader can
// still read who holds it: LockFileEx locks are mandatory on Windows.
const lockOffset = 1 << 30

// tryLock takes an exclusive, non-blocking lock on f. ok is false when
// another process holds it; err is set when the filesystem cannot lock.
func tryLock(f *os.File) (ok bool, err error) {
	ol := new(windows.Overlapped)
	ol.Offset = lockOffset
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) {
	ol := new(windows.Overlapped)
	ol.Offset = lockOffset
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
