package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/xmonader/janetcode/internal/shell"
)

// remoteHost is an SSH or Docker workspace: file ops and bash go through
// a local helper binary (ssh or docker exec) rather than the process's
// own filesystem.
type remoteHost struct {
	kind   string // ssh or docker
	target string // user@host or container
	root   string
}

func (h *remoteHost) Kind() string { return h.kind }
func (h *remoteHost) Root() string { return h.root }
func (h *remoteHost) Close() error { return nil }
func (h *remoteHost) Label() string {
	switch h.kind {
	case "docker":
		return "docker:" + h.target + ":" + h.root
	default:
		return h.target + ":" + h.root
	}
}

func (h *remoteHost) argv(ctx context.Context, stdin io.Reader, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var cmd *exec.Cmd
	switch h.kind {
	case "docker":
		a := append([]string{"exec", "-i", h.target}, args...)
		cmd = exec.CommandContext(ctx, "docker", a...)
	default:
		a := append(sshArgs(h.target), args...)
		cmd = exec.CommandContext(ctx, "ssh", a...)
	}
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

func (h *remoteHost) ReadFile(ctx context.Context, path string) ([]byte, error) {
	out, err := h.argv(ctx, nil, "cat", "--", path)
	if err != nil {
		return nil, remoteErr("read", path, out, err)
	}
	return out, nil
}

func (h *remoteHost) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error {
	if err := h.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := h.argv(ctx, bytes.NewReader(data), "tee", path)
	if err != nil {
		return remoteErr("write", path, out, err)
	}
	if perm != 0 {
		mode := fmt.Sprintf("%04o", perm.Perm())
		if out, err := h.argv(ctx, nil, "chmod", mode, path); err != nil {
			return remoteErr("chmod", path, out, err)
		}
	}
	return nil
}

func (h *remoteHost) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	if out, err := h.argv(ctx, nil, "test", "-d", path); err == nil {
		return remoteInfo{name: filepath.Base(path), dir: true}, nil
	} else if isNotExistOutput(string(out)) {
		return nil, os.ErrNotExist
	}
	if _, err := h.argv(ctx, nil, "test", "-e", path); err != nil {
		return nil, os.ErrNotExist
	}
	return remoteInfo{name: filepath.Base(path), dir: false}, nil
}

func (h *remoteHost) ReadDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	out, err := h.argv(ctx, nil, "ls", "-1Ap", path)
	if err != nil {
		return nil, remoteErr("list", path, out, err)
	}
	var entries []os.DirEntry
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "./" || line == "../" {
			continue
		}
		dir := strings.HasSuffix(line, "/")
		name := strings.TrimSuffix(line, "/")
		entries = append(entries, remoteEntry{name: name, dir: dir})
	}
	return entries, nil
}

func (h *remoteHost) MkdirAll(ctx context.Context, path string, _ os.FileMode) error {
	if path == "" || path == "." || path == "/" {
		return nil
	}
	out, err := h.argv(ctx, nil, "mkdir", "-p", path)
	if err != nil {
		return remoteErr("mkdir", path, out, err)
	}
	return nil
}

func (h *remoteHost) Remove(ctx context.Context, path string) error {
	out, err := h.argv(ctx, nil, "rm", "-f", path)
	if err != nil {
		return remoteErr("remove", path, out, err)
	}
	return nil
}

func (h *remoteHost) Rename(ctx context.Context, oldpath, newpath string) error {
	out, err := h.argv(ctx, nil, "mv", "-f", oldpath, newpath)
	if err != nil {
		return remoteErr("rename", oldpath, out, err)
	}
	return nil
}

func (h *remoteHost) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	data, err := h.ReadFile(ctx, path)
	if err != nil {
		return nil, err
	}
	return readSeekCloser{bytes.NewReader(data)}, nil
}

type readSeekCloser struct{ *bytes.Reader }

func (readSeekCloser) Close() error { return nil }

func (h *remoteHost) WalkDir(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	out, err := h.argv(ctx, nil, "find", root)
	if err != nil {
		return remoteErr("walk", root, out, err)
	}
	for _, p := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		info, serr := h.Stat(ctx, p)
		d := remoteEntry{name: filepath.Base(p), dir: serr == nil && info.IsDir()}
		if err := fn(p, d, serr); err != nil {
			if err == fs.SkipDir {
				continue
			}
			return err
		}
	}
	return nil
}

func (h *remoteHost) Command(ctx context.Context, command string) (*exec.Cmd, error) {
	script := "cd " + shQuote(h.root) + " && " + command
	var cmd *exec.Cmd
	switch h.kind {
	case "docker":
		cmd = exec.CommandContext(ctx, "docker", "exec", "-i", "-w", h.root, h.target, "bash", "-lc", command)
	default:
		cmd = exec.CommandContext(ctx, "ssh", append(sshArgs(h.target), "bash", "-lc", script)...)
	}
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
	return cmd, nil
}

func sshArgs(target string) []string {
	return []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=10", target, "--"}
}

// ParseSSHSpec splits user@host[:path] into the ssh target and an optional
// remote working directory. A trailing :digits is a port, which belongs
// in ~/.ssh/config rather than here.
func ParseSSHSpec(spec string) (target, path string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("usage: /ssh user@host[:/path]")
	}
	if i := strings.LastIndex(spec, ":"); i > 0 {
		rest := spec[i+1:]
		if rest == "" {
			return spec[:i], "", nil
		}
		if allDigits(rest) {
			return "", "", fmt.Errorf("a port in the spec is not supported; put Port in ~/.ssh/config for %s", spec[:i])
		}
		return spec[:i], rest, nil
	}
	return spec, "", nil
}

// ParseDockerSpec splits container[:/path] or "container /path".
func ParseDockerSpec(spec string) (container, path string, err error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", "", fmt.Errorf("usage: /docker <container>[:/path]")
	}
	if i := strings.LastIndex(spec, ":/"); i >= 0 {
		return spec[:i], spec[i+1:], nil
	}
	fields := strings.Fields(spec)
	if len(fields) >= 2 {
		return fields[0], strings.Join(fields[1:], " "), nil
	}
	return spec, "", nil
}

// DialSSH probes user@host and returns a host rooted at path, or at the
// remote pwd when path is omitted.
func DialSSH(spec string) (Host, error) {
	target, path, err := ParseSSHSpec(spec)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append(sshArgs(target), "pwd")...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return nil, fmt.Errorf("ssh %s: %w", target, err)
		}
		return nil, fmt.Errorf("ssh %s: %s", target, msg)
	}
	pwd := strings.TrimSpace(string(out))
	if path == "" {
		path = pwd
	} else if strings.HasPrefix(path, "~") {
		home := pwd
		if homeOut, herr := exec.CommandContext(ctx, "ssh", append(sshArgs(target), "printf", "%s", "$HOME")...).CombinedOutput(); herr == nil && strings.TrimSpace(string(homeOut)) != "" {
			home = strings.TrimSpace(string(homeOut))
		}
		path = home + strings.TrimPrefix(path, "~")
	}
	return &remoteHost{kind: "ssh", target: target, root: path}, nil
}

// DialDocker probes a container and returns a host rooted at path, or at
// the container's pwd when path is omitted.
func DialDocker(spec string) (Host, error) {
	container, path, err := ParseDockerSpec(spec)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", container, "pwd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return nil, fmt.Errorf("docker %s: %w", container, err)
		}
		return nil, fmt.Errorf("docker %s: %s", container, msg)
	}
	if path == "" {
		path = strings.TrimSpace(string(out))
	}
	return &remoteHost{kind: "docker", target: container, root: path}, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

type remoteInfo struct {
	name string
	dir  bool
}

func (i remoteInfo) Name() string { return i.name }
func (i remoteInfo) Size() int64  { return 0 }
func (i remoteInfo) Mode() os.FileMode {
	if i.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (i remoteInfo) ModTime() time.Time { return time.Time{} }
func (i remoteInfo) IsDir() bool        { return i.dir }
func (i remoteInfo) Sys() any           { return nil }

type remoteEntry struct {
	name string
	dir  bool
}

func (e remoteEntry) Name() string      { return e.name }
func (e remoteEntry) IsDir() bool       { return e.dir }
func (e remoteEntry) Type() os.FileMode { return remoteInfo(e).Mode() }
func (e remoteEntry) Info() (os.FileInfo, error) {
	return remoteInfo(e), nil
}
