//go:build windows

package voice

import (
	"os"
	"os/exec"
)

func prepare(*exec.Cmd) {}

// shellArgv runs a configured recorder command through PowerShell.
func shellArgv(command string) []string {
	return []string{"powershell", "-NoProfile", "-Command", command}
}

// signalStop ends the recorder. Windows has no interrupt to send to a
// process outside the console's group, so it is killed; ffmpeg's -t bound
// and the endpoint's tolerance of a short WAV header cover the rest.
func signalStop(p *os.Process) error { return p.Kill() }
