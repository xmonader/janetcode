package tool

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Overlay is a dry run's copy-on-write layer over a workspace: while one
// is set (Workspace.SetOverlay), what the file tools write lands here
// instead of on the host, and what they read sees it. A dry-run turn can
// then edit a file twice, read back its own edit, and build a complete
// preview of the change without a byte reaching the disk. The shell and
// the search tools still see the disk: a command is not run in a dry run
// unless it only reads, and it reads the files as they are.
type Overlay struct {
	mu    sync.Mutex
	files map[string]overlayFile
	order []string // paths in the order they were first written
}

// overlayFile is one path's state in the overlay: its content, or gone
// when a dry-run call removed it.
type overlayFile struct {
	data []byte
	gone bool
}

// NewOverlay is an empty overlay.
func NewOverlay() *Overlay { return &Overlay{files: map[string]overlayFile{}} }

// OverlayChange is one path the overlay holds, as the preview and the
// apply step see it.
type OverlayChange struct {
	Path string
	Data []byte
	Gone bool
}

// Changes lists what the overlay holds, in the order the paths were first
// written.
func (o *Overlay) Changes() []OverlayChange {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]OverlayChange, 0, len(o.order))
	for _, p := range o.order {
		f := o.files[p]
		out = append(out, OverlayChange{Path: p, Data: append([]byte(nil), f.data...), Gone: f.gone})
	}
	return out
}

func (o *Overlay) get(p string) (overlayFile, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	f, ok := o.files[filepath.Clean(p)]
	return f, ok
}

func (o *Overlay) set(p string, f overlayFile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	p = filepath.Clean(p)
	if _, ok := o.files[p]; !ok {
		o.order = append(o.order, p)
	}
	o.files[p] = f
}

// children are the overlay paths directly inside dir.
func (o *Overlay) children(dir string) map[string]overlayFile {
	o.mu.Lock()
	defer o.mu.Unlock()
	dir = filepath.Clean(dir)
	out := map[string]overlayFile{}
	for p, f := range o.files {
		if filepath.Dir(p) == dir {
			out[filepath.Base(p)] = f
		}
	}
	return out
}

// Len is how many paths the overlay holds, deletions included.
func (o *Overlay) Len() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.files)
}

// under is every overlay path inside root, at any depth, sorted.
func (o *Overlay) under(root string) []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	root = filepath.Clean(root)
	var out []string
	for p := range o.files {
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// overlayHost is a Host whose file half goes through an Overlay first.
type overlayHost struct {
	Host
	o *Overlay
}

func (h overlayHost) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if f, ok := h.o.get(path); ok {
		if f.gone {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return append([]byte(nil), f.data...), nil
	}
	return h.Host.ReadFile(ctx, path)
}

func (h overlayHost) WriteFile(_ context.Context, path string, data []byte, _ os.FileMode) error {
	h.o.set(path, overlayFile{data: append([]byte(nil), data...)})
	return nil
}

func (h overlayHost) Stat(ctx context.Context, path string) (os.FileInfo, error) {
	if f, ok := h.o.get(path); ok {
		if f.gone {
			return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
		}
		return overlayInfo{name: filepath.Base(path), size: int64(len(f.data))}, nil
	}
	st, err := h.Host.Stat(ctx, path)
	if err != nil && len(h.o.children(path)) > 0 {
		// A directory only the overlay's writes have created.
		return overlayInfo{name: filepath.Base(path), dir: true}, nil
	}
	return st, err
}

func (h overlayHost) ReadDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	entries, err := h.Host.ReadDir(ctx, path)
	kids := h.o.children(path)
	if err != nil && len(kids) == 0 {
		return nil, err
	}
	var out []os.DirEntry
	for _, e := range entries {
		if f, ok := kids[e.Name()]; ok {
			if !f.gone {
				out = append(out, fs.FileInfoToDirEntry(overlayInfo{name: e.Name(), size: int64(len(f.data))}))
			}
			delete(kids, e.Name())
			continue
		}
		out = append(out, e)
	}
	for name, f := range kids {
		if !f.gone {
			out = append(out, fs.FileInfoToDirEntry(overlayInfo{name: name, size: int64(len(f.data))}))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// WalkDir walks the tree as the dry run sees it, so glob and grep find
// what the model has written in it: a file the overlay changed is visited
// with its pending size (and grep then opens the pending content), a file
// it deleted is not visited, and a file only the overlay holds is visited
// after the walk of the disk, unless it sits under a directory fn skipped.
func (h overlayHost) WalkDir(ctx context.Context, root string, fn fs.WalkDirFunc) error {
	root = filepath.Clean(root)
	pending := h.o.under(root)
	visited := map[string]bool{}
	var skipped []string
	wrap := func(p string, d fs.DirEntry, err error) error {
		if err == nil && d != nil && !d.IsDir() {
			if f, ok := h.o.get(p); ok {
				visited[p] = true
				if f.gone {
					return nil
				}
				d = fs.FileInfoToDirEntry(overlayInfo{name: d.Name(), size: int64(len(f.data))})
			}
		}
		r := fn(p, d, err)
		if r == fs.SkipDir && d != nil && d.IsDir() {
			skipped = append(skipped, filepath.Clean(p))
		}
		return r
	}
	if _, err := h.Host.Stat(ctx, root); err == nil || len(pending) == 0 {
		if err := h.Host.WalkDir(ctx, root, wrap); err != nil {
			if err == fs.SkipAll {
				return nil
			}
			return err
		}
	}
	for _, p := range pending {
		f, _ := h.o.get(p)
		if visited[p] || f.gone || underAny(p, skipped) {
			continue
		}
		switch err := fn(p, fs.FileInfoToDirEntry(overlayInfo{name: filepath.Base(p), size: int64(len(f.data))}), nil); err {
		case nil, fs.SkipDir:
		case fs.SkipAll:
			return nil
		default:
			return err
		}
	}
	return nil
}

// underAny reports whether p is inside one of dirs.
func underAny(p string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(p, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// MkdirAll is a no-op: the overlay's files imply their directories.
func (h overlayHost) MkdirAll(context.Context, string, os.FileMode) error { return nil }

func (h overlayHost) Remove(ctx context.Context, path string) error {
	if _, err := h.Stat(ctx, path); err != nil {
		return err
	}
	h.o.set(path, overlayFile{gone: true})
	return nil
}

func (h overlayHost) Rename(ctx context.Context, oldpath, newpath string) error {
	data, err := h.ReadFile(ctx, oldpath)
	if err != nil {
		return err
	}
	h.o.set(newpath, overlayFile{data: data})
	h.o.set(oldpath, overlayFile{gone: true})
	return nil
}

func (h overlayHost) Open(ctx context.Context, path string) (io.ReadCloser, error) {
	if f, ok := h.o.get(path); ok {
		if f.gone {
			return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrNotExist}
		}
		return io.NopCloser(bytes.NewReader(append([]byte(nil), f.data...))), nil
	}
	return h.Host.Open(ctx, path)
}

// overlayInfo is the FileInfo of a path the overlay holds.
type overlayInfo struct {
	name string
	size int64
	dir  bool
}

func (i overlayInfo) Name() string { return i.name }
func (i overlayInfo) Size() int64  { return i.size }
func (i overlayInfo) Mode() os.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i overlayInfo) ModTime() time.Time { return time.Time{} }
func (i overlayInfo) IsDir() bool        { return i.dir }
func (i overlayInfo) Sys() any           { return nil }

// WorkspaceBound is implemented by a tool whose file access goes through
// a Workspace, so a dry run can tell a tool whose writes an overlay
// catches from one it has to stop outright.
type WorkspaceBound interface {
	Workspace() *Workspace
}

// Workspace implements WorkspaceBound.
func (t WriteFile) Workspace() *Workspace { return t.WS }

// Workspace implements WorkspaceBound.
func (t EditFile) Workspace() *Workspace { return t.WS }
