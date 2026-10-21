//go:build !windows

package voice

import (
	"os"
	"os/exec"
	"syscall"
)

// prepare puts the recorder in a process group of its own, so the stop
// reaches a configured recorder's children too (a shell that runs one).
func prepare(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// shellArgv runs a configured recorder command through the shell.
func shellArgv(command string) []string { return []string{"/bin/sh", "-c", command} }

// signalStop asks the recorder to finish, as ctrl+c at its terminal would.
func signalStop(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGINT); err != nil {
		return p.Signal(os.Interrupt)
	}
	return nil
}

// kill ends the recorder and everything it started. Killing the shell
// alone left a configured recorder's child running, still recording and
// holding stderr open, so waiting for the recorder never ended.
func kill(p *os.Process) error {
	if err := syscall.Kill(-p.Pid, syscall.SIGKILL); err != nil {
		return p.Kill()
	}
	return nil
}
