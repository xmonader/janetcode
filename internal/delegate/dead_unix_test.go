//go:build !windows

package delegate

import (
	"syscall"
	"testing"
	"time"
)

func assertDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("process %d is still alive", pid)
}
