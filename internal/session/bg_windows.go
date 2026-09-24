//go:build windows

package session

import "os"

// processAlive reports whether pid is a running process: on Windows,
// FindProcess opens a handle and fails when there is no such process.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	_ = p.Release()
	return true
}
