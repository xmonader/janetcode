package tool

import (
	"context"
	"os"
	"os/exec"

	"github.com/xmonader/janetcode/internal/shell"
)

// localShell builds the exec.Cmd for a command: a remote workspace uses
// the host, an enabled sandbox wraps docker/sandbox-exec, otherwise the
// default shell in root.
func localShell(ctx context.Context, ws *Workspace, sb *shell.Sandbox, root, command string) (*exec.Cmd, error) {
	if ws != nil && !ws.Local() {
		return ws.Host().Command(ctx, command)
	}
	if sb != nil && sb.Enabled() {
		// The directories granted with /add-dir are write roots of the
		// active profile too, so a sandboxed command can work in them.
		var extra []string
		if ws != nil {
			extra = ws.ExtraDirs()
		}
		return sb.CommandIn(ctx, root, extra, command)
	}
	cmd := shell.Command(ctx, command)
	cmd.Dir = root
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
	return cmd, nil
}
