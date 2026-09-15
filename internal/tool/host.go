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

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/shell"
)

// FS is the file half of a host: what the file tools read and write
// through. Every call takes a context so a remote host's ssh or docker
// helper can be cancelled with the tool call that started it.
type FS interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode) error
	Stat(ctx context.Context, path string) (os.FileInfo, error)
	ReadDir(ctx context.Context, path string) ([]os.DirEntry, error)
	MkdirAll(ctx context.Context, path string, perm os.FileMode) error
	Remove(ctx context.Context, path string) error
	Rename(ctx context.Context, oldpath, newpath string) error
	Open(ctx context.Context, path string) (io.ReadCloser, error)
	WalkDir(ctx context.Context, root string, fn fs.WalkDirFunc) error
}

// Commander is the shell half of a host: what bash runs through.
type Commander interface {
	// Command is a local process that runs command in Root on this host.
	// Stdin/Stdout/Stderr are left unset for the caller to attach.
	Command(ctx context.Context, command string) (*exec.Cmd, error)
}

// Host is the machine the file and shell tools talk to. Local is the
// process's own filesystem; SSH and Docker retarget the same tools in
// place because they share one Workspace pointer rather than a copied
// Root string.
type Host interface {
	Kind() string
	Label() string
	Root() string
	Close() error

	FS
	Commander
}

// Workspace is the working directory and host the file and shell tools
// use. One pointer is shared so /ssh and /docker retarget every tool
// without rebuilding the agent.
type Workspace struct {
	mu    sync.RWMutex
	host  Host
	extra []string
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

// AddDir grants file tools an extra local root for this session. The
// path must exist as a directory. Relative paths stay relative to the
// working directory; extra roots mainly admit absolute paths under them
// (and relatives that resolve there, such as ../sibling/file).
func (w *Workspace) AddDir(dir string) error {
	if w == nil {
		return fmt.Errorf("no workspace")
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, e := range w.extra {
		if e == abs {
			return nil
		}
	}
	w.extra = append(w.extra, abs)
	return nil
}

// ExtraDirs is a copy of the additional file-tool roots.
func (w *Workspace) ExtraDirs() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]string(nil), w.extra...)
}

// Roots is the working directory followed by extra dirs.
func (w *Workspace) Roots() []string {
	r := w.Root()
	extra := w.ExtraDirs()
	if r == "" {
		return extra
	}
	return append([]string{r}, extra...)
}

// Retarget applies a /ssh or /docker argument to ws: ""/"status" reports
// the current workspace, "off"/"local" returns it to the local
// filesystem at cwd, and anything else dials a new host with dial. kind
// names the command in the dial error ("ssh" or "docker").
func Retarget(ws *Workspace, cwd, kind, arg string, dial func(string) (Host, error)) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("this session has no retargetable workspace")
	}
	switch arg {
	case "", "status":
		return "workspace " + ws.Label(), nil
	case "off", "local":
		ws.Set(LocalHost(cwd))
		return "workspace local · " + cwd, nil
	}
	h, err := dial(arg)
	if err != nil {
		return "", fmt.Errorf("%s: %w", kind, err)
	}
	ws.Set(h)
	return "workspace " + h.Label() + " · file tools and bash run there", nil
}

// Local is the process's own filesystem and shell.
type Local struct{ root string }

func (l Local) Kind() string  { return "local" }
func (l Local) Label() string { return l.root }
func (l Local) Root() string  { return l.root }
func (Local) Close() error    { return nil }

func (Local) ReadFile(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }
func (Local) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data)
}
func (Local) Stat(_ context.Context, path string) (os.FileInfo, error) { return os.Stat(path) }
func (Local) ReadDir(_ context.Context, path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}
func (Local) MkdirAll(_ context.Context, path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}
func (Local) Remove(_ context.Context, path string) error { return os.Remove(path) }
func (Local) Rename(_ context.Context, oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}
func (Local) Open(_ context.Context, path string) (io.ReadCloser, error) { return os.Open(path) }
func (Local) WalkDir(_ context.Context, root string, fn fs.WalkDirFunc) error {
	return filepath.WalkDir(root, fn)
}
func (l Local) Command(ctx context.Context, command string) (*exec.Cmd, error) {
	cmd := shell.Command(ctx, command)
	cmd.Dir = l.root
	cmd.Env = shell.ScrubEnv(os.Environ())
	shell.SetProcessGroup(cmd)
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
	eval := ws == nil || ws.Local()
	roots := []string{r}
	if ws != nil && ws.Local() {
		if extra := ws.ExtraDirs(); len(extra) > 0 {
			roots = append([]string{r}, extra...)
		}
	}
	return confineIn(roots, p, eval)
}

func readFileOf(ctx context.Context, ws *Workspace, path string) ([]byte, error) {
	if ws != nil {
		return ws.Host().ReadFile(ctx, path)
	}
	return os.ReadFile(path)
}

func writeFileOf(ctx context.Context, ws *Workspace, path string, data []byte) error {
	if ws != nil {
		return ws.Host().WriteFile(ctx, path, data, 0o644)
	}
	return fsutil.AtomicWrite(path, data)
}

func statOf(ctx context.Context, ws *Workspace, path string) (os.FileInfo, error) {
	if ws != nil {
		return ws.Host().Stat(ctx, path)
	}
	return os.Stat(path)
}

func readDirOf(ctx context.Context, ws *Workspace, path string) ([]os.DirEntry, error) {
	if ws != nil {
		return ws.Host().ReadDir(ctx, path)
	}
	return os.ReadDir(path)
}

func mkdirAllOf(ctx context.Context, ws *Workspace, path string, perm os.FileMode) error {
	if ws != nil {
		return ws.Host().MkdirAll(ctx, path, perm)
	}
	return os.MkdirAll(path, perm)
}

func openOf(ctx context.Context, ws *Workspace, path string) (io.ReadCloser, error) {
	if ws != nil {
		return ws.Host().Open(ctx, path)
	}
	return os.Open(path)
}

func walkDirOf(ctx context.Context, ws *Workspace, root string, fn fs.WalkDirFunc) error {
	if ws != nil {
		return ws.Host().WalkDir(ctx, root, fn)
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
