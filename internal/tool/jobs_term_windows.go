//go:build windows

package tool

import (
	"github.com/xmonader/janetcode/internal/shell"

	"errors"
	"os"
	"os/exec"
)

var errNoTerminal = errors.New("no pseudo-terminal on this system")

// startTerminal has no pseudo-terminal to offer on Windows; the caller
// falls back to a pipe and says so.
func (j *Jobs) startTerminal(id int, command string, cmd *exec.Cmd, out *os.File, guard *shell.GitGuard) (*Job, error) {
	return nil, errNoTerminal
}
