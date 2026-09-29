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
	// overlay, while set, catches the file tools' writes (a dry run; see
	// Overlay). Host hands it out wrapped around the real host.
	overlay *Overlay
}

// SetOverlay starts (o != nil) or ends (nil) a dry run's overlay: from
// now on the file tools write into o instead of the host.
func (w *Workspace) SetOverlay(o *Overlay) {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.overlay = o
}

// Overlay is the dry run's overlay, or nil when there is none.
func (w *Workspace) Overlay() *Overlay {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.overlay
}

// RealHost is the host under any overlay: where a dry run's preview is
// written once the user applies it.
func (w *Workspace) RealHost() Host {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.host
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
	if w.overlay != nil && w.host != nil {
		return overlayHost{Host: w.host, o: w.overlay}
	}
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

func (Local) ReadFile(_ context.Context, path string) ([]byte, error) {
	if err := checkFinalComponent(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
func (Local) WriteFile(_ context.Context, path string, data []byte, perm os.FileMode) error {
	if err := checkFinalComponent(path); err != nil {
		return err
	}
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
func (Local) Open(_ context.Context, path string) (io.ReadCloser, error) {
	if err := checkFinalComponent(path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

// checkFinalComponent refuses a path whose final component is not a
// regular file: reading a FIFO blocks until some other process writes
// (hanging the tool until an outer timeout), and a device or socket is
// not file content.
//
// A symlink is judged by what it points at, not by being a symlink.
// Exempting links outright and leaving them to the confinement check was
// a hole: confinement asks whether the target is inside the workspace,
// and an in-workspace FIFO is inside the workspace, so link -> pipe
// passed and the read blocked exactly as a bare FIFO would. A link to a
// regular file still reads, which is the case the exemption was for.
//
// An absent, dangling or unreadable path returns nil: the open itself
// reports it, with the error the caller already handles.
func checkFinalComponent(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if st.Mode()&os.ModeSymlink != 0 {
		// Follow it. A dangling link fails the Stat and is left to the
		// open, which reports it better than this can.
		if st, err = os.Stat(path); err != nil {
			return nil
		}
	}
	if st.Mode().IsRegular() {
		return nil
	}
	kind := "file"
	switch {
	case st.Mode()&fs.ModeNamedPipe != 0:
		kind = "FIFO"
	case st.Mode()&fs.ModeDevice != 0:
		kind = "device file"
	case st.Mode()&fs.ModeSocket != 0:
		kind = "socket"
	case st.IsDir():
		kind = "directory"
	}
	return fmt.Errorf("%s is a %s, not a regular file", path, kind)
}
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
	// Local applies this guard for itself; a nil workspace used to reach
	// the os with nothing in the way, and subagent worktrees and minimal
	// mode are both wired that way.
	if err := checkFinalComponent(path); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func writeFileOf(ctx context.Context, ws *Workspace, path string, data []byte) error {
	if ws != nil {
		return ws.Host().WriteFile(ctx, path, data, 0o644)
	}
	if err := checkFinalComponent(path); err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, data)
}

func statOf(ctx context.Context, ws *Workspace, path string) (os.FileInfo, error) {
	if ws != nil {
		return ws.Host().Stat(ctx, path)
	}
	return os.Stat(path)
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
	if err := checkFinalComponent(path); err != nil {
		return nil, err
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
