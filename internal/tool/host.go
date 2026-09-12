package tool

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Host is the machine the file and shell tools talk to. Local is the
// process's own filesystem; SSH and Docker retarget the same tools in
// place because they share one Workspace pointer rather than a copied
// Root string.
type Host interface {
	Kind() string
	Label() string
	Root() string
	Close() error

	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Stat(path string) (os.FileInfo, error)
	ReadDir(path string) ([]os.DirEntry, error)
	MkdirAll(path string, perm os.FileMode) error
	Remove(path string) error
	Rename(oldpath, newpath string) error
	Open(path string) (io.ReadCloser, error)
	WalkDir(root string, fn fs.WalkDirFunc) error

	// Command is a local process that runs command in Root on this host.
	// Stdin/Stdout/Stderr are left unset for the caller to attach.
	Command(ctx context.Context, command string) (*exec.Cmd, error)
}

// Workspace is the working directory and host the file and shell tools
// use. One pointer is shared so /ssh and /docker retarget every tool
// without rebuilding the agent.
type Workspace struct {
	mu   sync.RWMutex
	host Host
}

// NewLocal builds a workspace on the process's own filesystem.
func NewLocal(root string) *Workspace {
	return &Workspace{host: LocalHost(root)}
}

// LocalHost is the process's own filesystem at root.
func LocalHost(root string) Host { return Local{root: root} }

// Host is the current backend. It is never nil after NewLocal.
func (w *Workspace) Host() Host {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.host
}

// Root is the working directory on the current host.
func (w *Workspace) Root() string {
	if h := w.Host(); h != nil {
		return h.Root()
	}
	return ""
}

// Kind is "local", "ssh" or "docker".
func (w *Workspace) Kind() string {
	if h := w.Host(); h != nil {
		return h.Kind()
	}
	return "local"
}

// Local reports whether tools should keep using the process's own OS
// (the historical path). A nil workspace is local.
func (w *Workspace) Local() bool {
	return w == nil || w.Kind() == "local"
}

// Label is what the header shows: a local path, or user@host:/path.
func (w *Workspace) Label() string {
	if h := w.Host(); h != nil {
		return h.Label()
	}
	return ""
}

// Set replaces the backend. The previous host is closed.
func (w *Workspace) Set(h Host) {
	if w == nil || h == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.host != nil {
		_ = w.host.Close()
	}
	w.host = h
}

// Local is the process's own filesystem and shell.
type Local struct{ root string }

func (l Local) Kind() string  { return "local" }
func (l Local) Label() string { return l.root }
func (l Local) Root() string  { return l.root }
func (Local) Close() error    { return nil }

func (Local) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (Local) WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return atomicWrite(path, data)
}
func (Local) Stat(path string) (os.FileInfo, error)      { return os.Stat(path) }
func (Local) ReadDir(path string) ([]os.DirEntry, error) { return os.ReadDir(path) }
func (Local) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
func (Local) Remove(path string) error { return os.Remove(path) }
func (Local) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
func (Local) Open(path string) (io.ReadCloser, error) { return os.Open(path) }
func (Local) WalkDir(root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}
func (l Local) Command(ctx context.Context, command string) (*exec.Cmd, error) {
	cmd := ShellCommand(ctx, command)
	cmd.Dir = l.root
	cmd.Env = scrubEnv(os.Environ())
	setProcessGroup(cmd)
	return cmd, nil
}

func rootOf(root string, ws *Workspace) string {
	if ws != nil {
		return ws.Root()
	}
	return root
}

func resolveOn(ws *Workspace, root, p string) (string, error) {
	r := rootOf(root, ws)
	if ws != nil && !ws.Local() {
		return confinePath(r, p, false)
	}
	return confinePath(r, p, true)
}

func readFileOf(ws *Workspace, path string) ([]byte, error) {
	if ws != nil {
		return ws.Host().ReadFile(path)
	}
	return os.ReadFile(path)
}

func writeFileOf(ws *Workspace, path string, data []byte) error {
	if ws != nil {
		return ws.Host().WriteFile(path, data, 0o644)
	}
	return atomicWrite(path, data)
}

func statOf(ws *Workspace, path string) (os.FileInfo, error) {
	if ws != nil {
		return ws.Host().Stat(path)
	}
	return os.Stat(path)
}

func readDirOf(ws *Workspace, path string) ([]os.DirEntry, error) {
	if ws != nil {
		return ws.Host().ReadDir(path)
	}
	return os.ReadDir(path)
}

func mkdirAllOf(ws *Workspace, path string, perm os.FileMode) error {
	if ws != nil {
		return ws.Host().MkdirAll(path, perm)
	}
	return os.MkdirAll(path, perm)
}

func openOf(ws *Workspace, path string) (io.ReadCloser, error) {
	if ws != nil {
		return ws.Host().Open(path)
	}
	return os.Open(path)
}

func walkDirOf(ws *Workspace, root string, fn fs.WalkDirFunc) error {
	if ws != nil {
		return ws.Host().WalkDir(root, fn)
	}
	return filepath.WalkDir(root, fn)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func remoteErr(op, path string, out []byte, err error) error {
	msg := strings.TrimSpace(string(out))
	if isNotExistOutput(msg) {
		return os.ErrNotExist
	}
	if msg == "" {
		return fmt.Errorf("%s %s: %w", op, path, err)
	}
	return fmt.Errorf("%s %s: %s", op, path, msg)
}

func isNotExistOutput(msg string) bool {
	low := strings.ToLower(msg)
	return strings.Contains(low, "no such file") || strings.Contains(low, "not found") || strings.Contains(low, "not a directory")
}
