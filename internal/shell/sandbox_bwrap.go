package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// bwrap builds the bubblewrap command that carries the profile on Linux:
// the whole filesystem read-only, the write roots bound read-write on
// top, each deny glob with a literal directory re-bound read-only on top
// of that, a fresh /dev and /proc, its own PID namespace, and for a
// profile with no network its own network namespace (loopback only).
//
// Unlike docker the command runs against this machine's toolchain, and
// unlike Landlock the network goes entirely (UDP and DNS included) on
// any kernel and a deny glob is enforced by a mount, not only by reading
// the command line. --die-with-parent with the PID namespace means
// killing the bwrap process (a timeout, ^C) kills everything the command
// started: the namespace's init goes, and the kernel takes the rest.
func (s *Sandbox) bwrap(ctx context.Context, p Profile, root string, extra []string, command string) (*exec.Cmd, error) {
	path, err := s.lookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bwrap not found")
	}
	cmd := exec.CommandContext(ctx, path, bwrapArgs(p, root, extra, os.TempDir(), command)...)
	cmd.Dir = root
	cmd.Env = ScrubEnv(os.Environ())
	SetProcessGroup(cmd)
	return cmd, nil
}

func bwrapArgs(p Profile, root string, extra []string, tempDir, command string) []string {
	args := []string{
		"--die-with-parent",
		// A new session: a sandboxed command cannot push keystrokes into
		// the terminal janetcode runs in (TIOCSTI).
		"--new-session",
		"--unshare-pid",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
	}
	if !p.Network {
		args = append(args, "--unshare-net")
	}
	roots := p.WritableRoots(root, extra, tempDir)
	for _, r := range roots {
		// A root that is gone (an /add-dir removed since) cannot be
		// bound; bwrap would refuse to start at all.
		if isDir(r) {
			args = append(args, "--bind", r, r)
		}
	}
	// Later mounts sit on top of earlier ones, so a protected directory
	// inside a write root is read-only again. One that does not exist yet
	// is left to the command analysis: creating it here would put an
	// empty .git or .janetcode into the user's project.
	for _, g := range p.DenyGlobs {
		head := literalDenyPrefix(g)
		if head == "" {
			continue
		}
		for _, r := range roots {
			d := filepath.Join(r, head)
			if isDir(d) {
				args = append(args, "--ro-bind", d, d)
			}
		}
	}
	// .git stays writable for commits, but what runs code on the host
	// later does not: a planted hook, or a config with core.fsmonitor or
	// core.sshCommand, fires the next time git runs outside the sandbox.
	for _, r := range roots {
		for _, rel := range []string{".git/config", ".git/hooks"} {
			if p := filepath.Join(r, rel); exists(p) {
				args = append(args, "--ro-bind", p, p)
			}
		}
	}
	sh := Default()
	args = append(args, "--chdir", root, "--", sh.Path)
	args = append(args, sh.Args...)
	return append(args, command)
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// bwrapWorks runs bwrap once: it is installed on many machines where it
// cannot run (unprivileged user namespaces off, or an AppArmor policy
// that refuses them), and picking it there would fail every command.
var (
	bwrapOnce sync.Once
	bwrapOK   bool
)

func (s *Sandbox) bwrapUsable() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	path, err := s.lookPath("bwrap")
	if err != nil {
		return false
	}
	if s != nil && s.probe != nil {
		return s.probe()
	}
	if s != nil && s.look != nil {
		return false // a test stubbing the lookup has not said bwrap runs
	}
	bwrapOnce.Do(func() {
		bwrapOK = exec.Command(path, "--die-with-parent", "--unshare-pid", "--unshare-net", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "true").Run() == nil
	})
	return bwrapOK
}
