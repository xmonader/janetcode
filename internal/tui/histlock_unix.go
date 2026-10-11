//go:build !windows

package tui

import (
	"os"
	"syscall"
)

// lockHistory waits for the exclusive advisory lock on the history's
// lock file.
func lockHistory(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func unlockHistory(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
