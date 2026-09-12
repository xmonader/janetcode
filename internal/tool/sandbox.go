package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// DefaultSandboxImage is used when /sandbox is on and no image was named.
const DefaultSandboxImage = "alpine:3.20"

// Sandbox, when enabled, wraps local bash in docker (Linux) or
// sandbox-exec (macOS). File tools stay on the workspace host — including
// after /ssh or /docker — so the sandbox is only around the shell.
// Off by default.
type Sandbox struct {
	mu    sync.Mutex
	on    bool
	image string
	// look, when set, replaces exec.LookPath (tests).
	look func(string) (string, error)
}

// Enabled reports whether wrapping is on.
func (s *Sandbox) Enabled() bool {
	return s != nil && s.onLocked()
}

func (s *Sandbox) onLocked() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on
}

// Image is the docker image, or the default.
func (s *Sandbox) Image() string {
	if s == nil {
		return DefaultSandboxImage
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.image == "" {
		return DefaultSandboxImage
	}
	return s.image
}

// SetImage names the docker image used when wrapping. Empty restores the default.
func (s *Sandbox) SetImage(image string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.image = strings.TrimSpace(image)
	s.mu.Unlock()
}

// Enable turns wrapping on after checking that the host can do it.
func (s *Sandbox) Enable() error {
	if s == nil {
		return fmt.Errorf("no sandbox")
	}
	if err := s.available(); err != nil {
		return err
	}
	s.mu.Lock()
	s.on = true
	s.mu.Unlock()
	return nil
}

// Disable turns wrapping off.
func (s *Sandbox) Disable() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.on = false
	s.mu.Unlock()
}

// Status is the one-line /sandbox report.
func (s *Sandbox) Status() string {
	if s == nil || !s.Enabled() {
		return "sandbox off · bash runs on the workspace host"
	}
	switch runtime.GOOS {
	case "darwin":
		return "sandbox on · bash runs under sandbox-exec"
	default:
		return "sandbox on · bash runs in docker (" + s.Image() + ")"
	}
}

func (s *Sandbox) lookPath(name string) (string, error) {
	if s != nil && s.look != nil {
		return s.look(name)
	}
	return exec.LookPath(name)
}

func (s *Sandbox) available() error {
	switch runtime.GOOS {
	case "windows":
		return fmt.Errorf("sandbox is not supported on windows")
	case "darwin":
		if _, err := s.lookPath("sandbox-exec"); err != nil {
			return fmt.Errorf("sandbox-exec not found")
		}
		return nil
	default:
		if _, err := s.lookPath("docker"); err != nil {
			return fmt.Errorf("docker not found (needed to sandbox bash)")
		}
		return nil
	}
}

// Command wraps command so it runs inside the sandbox, cwd root.
func (s *Sandbox) Command(ctx context.Context, root, command string) (*exec.Cmd, error) {
	if s == nil || !s.Enabled() {
		return nil, fmt.Errorf("sandbox is off")
	}
	if err := s.available(); err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" {
		return s.seatbelt(ctx, root, command)
	}
	return s.docker(ctx, root, command)
}

func (s *Sandbox) docker(ctx context.Context, root, command string) (*exec.Cmd, error) {
	bin, err := s.lookPath("docker")
	if err != nil {
		return nil, err
	}
	image := s.Image()
	args := []string{
		"run", "--rm", "-i",
		"--network", "bridge",
		"-v", root + ":" + root,
		"-w", root,
		"--tmpfs", "/tmp",
	}
	if uid := sandboxUser(); uid != "" {
		args = append(args, "-u", uid)
	}
	args = append(args, image, "sh", "-c", command)
	cmd := exec.CommandContext(ctx, bin, args...)
	setProcessGroup(cmd)
	return cmd, nil
}

func (s *Sandbox) seatbelt(ctx context.Context, root, command string) (*exec.Cmd, error) {
	bin, err := s.lookPath("sandbox-exec")
	if err != nil {
		return nil, err
	}
	profile := `(version 1)
(deny default)
(allow file-read*)
(allow file-write* (subpath "` + root + `"))
(allow file-write* (subpath "/tmp") (subpath "/private/tmp") (subpath "/var/tmp"))
(allow file-ioctl (regex #"^/dev/"))
(allow file-write-data (regex #"^/dev/"))
(allow process-exec*)
(allow process-fork)
(allow signal)
(allow sysctl-read)
(allow mach-lookup)
(allow network*)
(allow ipc-posix-shm)
`
	cmd := exec.CommandContext(ctx, bin, "-p", profile, "sh", "-c", command)
	cmd.Dir = root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	return cmd, nil
}

func sandboxUser() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", uid, gid)
}

// localShell builds the exec.Cmd for a command: a remote workspace uses
// the host, an enabled sandbox wraps docker/sandbox-exec, otherwise the
// default shell in root.
func localShell(ctx context.Context, ws *Workspace, sb *Sandbox, root, command string) (*exec.Cmd, error) {
	if ws != nil && !ws.Local() {
		return ws.Host().Command(ctx, command)
	}
	if sb != nil && sb.Enabled() {
		return sb.Command(ctx, root, command)
	}
	cmd := ShellCommand(ctx, command)
	cmd.Dir = root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	return cmd, nil
}
