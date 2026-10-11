//go:build windows

package tui

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockHistory waits for the exclusive lock on the history's lock file.
func lockHistory(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, new(windows.Overlapped))
}

func unlockHistory(f *os.File) {
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
