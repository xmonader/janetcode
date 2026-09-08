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

func signalExitCode(ee *exec.ExitError) int { return 1 }
