//go:build windows

package tool

import "os/exec"

func setProcessGroup(cmd *exec.Cmd) {}

func signalExitCode(ee *exec.ExitError) int { return 1 }
