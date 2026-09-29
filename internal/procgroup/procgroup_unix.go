//go:build unix

package procgroup

import (
	"os/exec"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// The group id is the leader's pid, because Setpgid made it one.
func terminateGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func killGroup(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// NoTerminal starts the command in a session of its own, which leaves it
// without a controlling terminal. Its process group is still its own
// (a session leader leads a group with its pid), so killGroup reaches
// it exactly as before.
//
// This is what keeps a model-run command from reading the terminal
// janetcode is drawing on. In a group of its own but the same session,
// a command that opens /dev/tty to ask for something — ssh's password,
// sudo, gpg's passphrase, an installer that insists on a human — is a
// background job of that terminal: the read stops it with SIGTTIN, and
// it sits there, stopped, until the timeout kills it minutes later.
// Without a controlling terminal the open fails at once with ENXIO, and
// the command says it has no terminal to ask on.
func NoTerminal(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	// setsid and setpgid cannot both be asked for: a session leader may
	// not change its group, so the child's setpgid fails with EPERM.
	cmd.SysProcAttr.Setpgid = false
	cmd.SysProcAttr.Setsid = true
}
