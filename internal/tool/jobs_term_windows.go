//go:build windows

package tool

import (
	"errors"
	"os"
	"os/exec"
)

var errNoTerminal = errors.New("no pseudo-terminal on this system")

// startTerminal has no pseudo-terminal to offer on Windows; the caller
// falls back to a pipe and says so.
func (j *Jobs) startTerminal(id int, command string, cmd *exec.Cmd, out *os.File) (*Job, error) {
	return nil, errNoTerminal
}
