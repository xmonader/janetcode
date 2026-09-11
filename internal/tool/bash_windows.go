//go:build windows

package tool

import (
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessGroup starts the command in its own process group and kills
// the whole tree on cancellation with taskkill, the closest Windows has to
// killing a process group. Untested on a real Windows machine.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

// processDone has no cheap answer here: Windows offers no "does this
// process exist" signal, and os.Process does not expose what it knows. A
// job that has been reaped is caught by its Done flag instead, a moment
// later than on unix. Untested on a real Windows machine, like the rest
// of this file.
func processDone(cmd *exec.Cmd) bool { return cmd == nil || cmd.Process == nil }

func signalExitCode(ee *exec.ExitError) int { return 1 }
