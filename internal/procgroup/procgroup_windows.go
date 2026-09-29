//go:build windows

package procgroup

import (
	"os/exec"
	"strconv"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}

// Windows has no signal that asks a tree to stop, so the polite step is
// taskkill without /F, which closes the windows of the tree; the forceful
// one adds /F. Untested on a real Windows machine, like the same path in
// internal/tool.
func terminateGroup(cmd *exec.Cmd) error {
	return exec.Command("taskkill", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}

func killGroup(cmd *exec.Cmd) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}

// NoTerminal is a no-op on Windows, where a child's console is chosen
// when it is created and a new process group already keeps it apart.
func NoTerminal(cmd *exec.Cmd) {}
