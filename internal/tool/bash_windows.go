//go:build windows

package tool

import (
	"os/exec"
)

// processDone has no cheap answer here: Windows offers no "does this
// process exist" signal, and os.Process does not expose what it knows. A
// job that has been reaped is caught by its Done flag instead, a moment
// later than on unix. Untested on a real Windows machine, like the rest
// of this file.
func processDone(cmd *exec.Cmd) bool { return cmd == nil || cmd.Process == nil }

func signalExitCode(ee *exec.ExitError) int { return 1 }
