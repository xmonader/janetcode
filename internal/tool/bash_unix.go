//go:build unix

package tool

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group so that
// cancellation kills the whole tree (pipelines, background servers).
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// processDone reports whether the process has already been waited for.
// os.Process knows this itself and answers under its own lock, so it is
// safe to ask from another goroutine — unlike cmd.ProcessState, which the
// waiting goroutine writes without synchronisation. Signal 0 checks for a
// process without sending one.
func processDone(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}
	return errors.Is(cmd.Process.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

func signalExitCode(ee *exec.ExitError) int {
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
