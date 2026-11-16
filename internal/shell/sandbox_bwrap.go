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
	env := ScrubEnv(os.Environ())
	var eg *bwrapEgress
	if port := s.proxyPort(p); port > 0 {
		if !egressBridgeSupported() {
			return nil, fmt.Errorf("the egress policy needs a bridge into bubblewrap's network namespace, which this build cannot make")
		}
		sock, err := s.egressProxy().UnixSocket()
		if err != nil {
			return nil, fmt.Errorf("egress policy: %w", err)
		}
		eg = &bwrapEgress{Sock: sock, Port: port}
		env = WithProxyEnv(env, fmt.Sprintf("http://127.0.0.1:%d", port))
	}
	args, err := bwrapArgs(p, root, extra, os.TempDir(), command, eg)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = root
	cmd.Env = env
	SetProcessGroup(cmd)
	return cmd, nil
}

// bwrapEgress is the way out a command in bubblewrap's own network
// namespace has under the egress policy: the proxy's unix socket,
// bridged to loopback on Port inside (see sandbox_egress_bridge_linux.go).
type bwrapEgress struct {
	Sock string
	Port int
}

func bwrapArgs(p Profile, root string, extra []string, tempDir, command string, eg *bwrapEgress) ([]string, error) {
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
	if !p.Network || eg != nil {
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
	if eg != nil {
		// Whatever else is mounted at the socket's directory, the proxy
		// stays reachable and nothing can replace it.
		d := filepath.Dir(eg.Sock)
		args = append(args, "--ro-bind", d, d)
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
	argv := append(append([]string{sh.Path}, sh.Args...), command)
	if eg != nil {
		var err error
		if argv, err = bridgeArgv(eg.Sock, eg.Port, argv); err != nil {
			return nil, err
		}
	}
	args = append(args, "--chdir", root, "--")
	return append(args, argv...), nil
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
