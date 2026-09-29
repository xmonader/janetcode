package tool

import (
	"context"
	"os"
	"os/exec"

	"github.com/xmonader/janetcode/internal/procgroup"
	"github.com/xmonader/janetcode/internal/shell"
)

// localShell builds the exec.Cmd for a command: a remote workspace uses
// the host, an enabled sandbox wraps docker/sandbox-exec, otherwise the
// default shell in root. Every one of them runs without a controlling
// terminal (procgroup.NoTerminal), so a program that asks on /dev/tty
// fails at once instead of stopping until the timeout.
func localShell(ctx context.Context, ws *Workspace, sb *shell.Sandbox, root, command string) (*exec.Cmd, error) {
	cmd, err := buildShell(ctx, ws, sb, root, command)
	if err != nil {
		return nil, err
	}
	procgroup.NoTerminal(cmd)
	return cmd, nil
}

func buildShell(ctx context.Context, ws *Workspace, sb *shell.Sandbox, root, command string) (*exec.Cmd, error) {
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

// sandboxGitGuard snapshots what in the repository's .git runs code later
// (hooks, config, the .git pointer), for a command about to run in the
// sandbox; nil when the command is not sandboxed. Its Restore runs when
// the command ends, however it ends.
func sandboxGitGuard(ws *Workspace, sb *shell.Sandbox, root string) *shell.GitGuard {
	if sb == nil || !sb.Enabled() || (ws != nil && !ws.Local()) {
		return nil
	}
	return shell.SnapshotGit(root)
}
