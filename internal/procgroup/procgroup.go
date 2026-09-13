// Package procgroup starts a child process in its own process group and
// stops the whole group, so nothing it spawned is left behind.
//
// Both of janetcode's long-lived child processes are wrappers more often
// than not. An MCP server configured as `npx -y some-server` is a node
// process that execs or forks the real server; a language server may
// respawn workers. Killing only the process janetcode started leaves the
// real one reparented to init, still holding the port, the index or —
// for rust-analyzer and clangd — a gigabyte of memory, for as long as the
// machine is up. internal/tool uses this too, for shell commands, where
// cancellation should hard-kill the whole group immediately rather than
// wait through the terminate-then-grace-then-kill sequence Stop gives the
// MCP and LSP transports — see KillOnCancel.
package procgroup

import (
	"os/exec"
	"time"
)

// Set puts cmd in its own process group. Call it before Start.
func Set(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	setProcessGroup(cmd)
}

// KillOnCancel puts cmd in its own process group and installs a Cancel
// hook that hard-kills the whole group as soon as the command's context is
// done — no terminate-then-grace step. That is what a shell command
// needs: a cancelled or timed-out command must stop immediately, along
// with anything it spawned, not be given time to clean up.
func KillOnCancel(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	Set(cmd)
	cmd.Cancel = func() error {
		return killGroup(cmd)
	}
}

// Stop ends the whole group: a termination signal first, so a server that
// wants to flush or unlink can, then a kill after grace, and finally a
// wait on done — the channel the caller closes once it has reaped the
// process — so the caller does not return while a zombie is still there.
//
// It must be called while the leader is still running. Once the leader has
// been reaped the kernel may hand its pid, and with it the group id, to
// something else, and the signal would land on a stranger; so a group
// whose leader is already gone is left alone rather than signalled blind.
func Stop(cmd *exec.Cmd, done <-chan struct{}, grace time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	select {
	case <-done:
		return
	default:
	}
	_ = terminateGroup(cmd)
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	_ = killGroup(cmd)
	select {
	case <-done:
	case <-time.After(grace):
	}
}
