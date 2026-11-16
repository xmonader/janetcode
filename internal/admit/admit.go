// Package admit is the session's file admission policy: an optional mode
// that answers "which files may the model change?" with a list the user
// can read, instead of with path confinement alone.
//
// Two modes. "workspace" is the default and changes nothing: the file
// tools edit anything under the working directory (and any /add-dir
// root) that the other rules allow. "explicit" narrows editing to the
// files and directories the user admitted; everything else is readable
// as before but refused for edits, with a message that names the command
// that admits it. Files can also be attached for read-only context: they
// are listed to the model as context and stay uneditable until admitted.
//
// Admission never widens what confinement allows. A path outside every
// root, or one that resolves through a symlink to a place the file tools
// may not go, stays refused whatever this policy says; the tool layer
// runs this check after, not instead of, that one.
//
// Entries are kept relative to the working directory when they lie under
// it, so the list follows a /cd or a worktree session to the same files
// in the new tree; paths elsewhere (an /add-dir root) are kept absolute.
package admit

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/ignore"
)

// Mode is how edits are governed.
type Mode string

const (
	// Workspace is the default: edits are governed by confinement alone.
	Workspace Mode = "workspace"
	// Explicit allows edits only to admitted files and directories.
	Explicit Mode = "explicit"
)

// ParseMode reads a mode name, accepting what a person would type.
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "workspace", "default", "off":
		return Workspace, nil
	case "explicit", "on":
		return Explicit, nil
	}
	return "", fmt.Errorf("unknown file mode %q: use workspace or explicit", s)
}

// State is the persisted form of a policy: the mode and the entries as
// the user admitted them. A directory entry ends in a slash.
type State struct {
	Mode   Mode     `json:"mode,omitempty"`
	Edit   []string `json:"edit,omitempty"`
	Attach []string `json:"attach,omitempty"`
}

// Zero reports whether the state is the default policy, so a session
// that never used admission writes nothing.
func (s State) Zero() bool {
	return (s.Mode == "" || s.Mode == Workspace) && len(s.Edit) == 0 && len(s.Attach) == 0
}

// Denied is the refusal for an edit to a file that is not admitted. It
// carries the command that fixes it, so every front end shows the same
// way out.
type Denied struct {
	Path     string // as the call named it
	Attached bool   // the file is attached read-only
}

func (d *Denied) Error() string {
	if d.Attached {
		return fmt.Sprintf("%s is attached as read-only context, and file mode is explicit: it may not be edited until the user admits it for editing with `/admit %s` (headless: --admit %s)", d.Path, d.Path, d.Path)
	}
	return fmt.Sprintf("%s is not admitted for editing: file mode is explicit, so only admitted files may change. The user can admit it with `/admit %s` (headless: --admit %s); do not work around this with another tool", d.Path, d.Path, d.Path)
}

// Policy is one session's admission state. The zero value is not usable;
// use New. It is safe for concurrent use.
type Policy struct {
	mu     sync.RWMutex
	mode   Mode
	edit   []string
	attach []string
}

// New is the default policy: workspace mode, nothing admitted.
func New() *Policy { return &Policy{mode: Workspace} }

// Mode is the current mode.
func (p *Policy) Mode() Mode {
	if p == nil {
		return Workspace
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.mode
}

// Explicit reports whether edits are restricted to admitted files.
func (p *Policy) Explicit() bool { return p.Mode() == Explicit }

// SetMode switches the mode.
func (p *Policy) SetMode(m Mode) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.mode = m
	p.mu.Unlock()
}

// Snapshot is the policy as persisted.
func (p *Policy) Snapshot() State {
	if p == nil {
		return State{}
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	st := State{Mode: p.mode, Edit: append([]string(nil), p.edit...), Attach: append([]string(nil), p.attach...)}
	if st.Mode == Workspace {
		st.Mode = ""
	}
	return st
}

// Restore replaces the policy with a persisted state.
func (p *Policy) Restore(st State) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = st.Mode
	if p.mode == "" {
		p.mode = Workspace
	}
	p.edit = append([]string(nil), st.Edit...)
	p.attach = append([]string(nil), st.Attach...)
}

// key is the stored spelling of abs: relative to root (slash form) when
// it lies under it, absolute otherwise; a directory ends in "/".
func key(root, abs string, isDir bool) string {
	k := filepath.ToSlash(abs)
	if root != "" {
		if rel, err := filepath.Rel(root, abs); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			k = filepath.ToSlash(rel)
		}
	}
	if isDir && !strings.HasSuffix(k, "/") {
		k += "/"
	}
	return k
}

// abs resolves a stored entry against root.
func abs(root, entry string) string {
	e := filepath.FromSlash(strings.TrimSuffix(entry, "/"))
	if filepath.IsAbs(e) || root == "" {
		return filepath.Clean(e)
	}
	return filepath.Join(root, e)
}

func isDirEntry(entry string) bool { return strings.HasSuffix(entry, "/") }

func has(list []string, k string) bool {
	for _, e := range list {
		if e == k {
			return true
		}
	}
	return false
}

// AddEdit admits abs (an already confined absolute path) for editing and
// reports whether the list changed. Admitting a file also lifts it from
// the read-only list.
func (p *Policy) AddEdit(root, path string) bool {
	k := key(root, path, isDir(path))
	p.mu.Lock()
	defer p.mu.Unlock()
	p.attach = without(p.attach, k)
	if has(p.edit, k) {
		return false
	}
	p.edit = append(p.edit, k)
	return true
}

// AddAttach attaches path as read-only context and reports whether the
// list changed. A file already admitted for editing stays admitted: it
// is context too.
func (p *Policy) AddAttach(root, path string) bool {
	k := key(root, path, isDir(path))
	p.mu.Lock()
	defer p.mu.Unlock()
	if has(p.edit, k) || has(p.attach, k) {
		return false
	}
	p.attach = append(p.attach, k)
	return true
}

// Remove drops path from both lists and reports whether it was there.
func (p *Policy) Remove(root, path string) bool {
	k := key(root, path, isDir(path))
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.edit) + len(p.attach)
	p.edit, p.attach = without(p.edit, k), without(p.attach, k)
	return len(p.edit)+len(p.attach) != n
}

// Clear empties both lists; the mode is left alone.
func (p *Policy) Clear() {
	p.mu.Lock()
	p.edit, p.attach = nil, nil
	p.mu.Unlock()
}

func without(list []string, k string) []string {
	out := list[:0:0]
	for _, e := range list {
		if e != k {
			out = append(out, e)
		}
	}
	return out
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// covers reports whether entry admits target (both absolute and clean):
// the same path, or a path beneath an admitted directory.
func covers(root, entry, target string) bool {
	e := abs(root, entry)
	if target == e {
		return true
	}
	if !isDirEntry(entry) {
		return false
	}
	rel, err := filepath.Rel(e, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// coveredBy reports whether any entry admits target, trying the entry as
// written and as the file system resolves it.
func coveredBy(root string, entries []string, target string) bool {
	for _, e := range entries {
		if covers(root, e, target) {
			return true
		}
		if real, err := filepath.EvalSymlinks(abs(root, e)); err == nil {
			re := e
			if isDirEntry(e) {
				re = filepath.ToSlash(real) + "/"
			} else {
				re = filepath.ToSlash(real)
			}
			if covers("", re, target) {
				return true
			}
		}
	}
	return false
}

// CheckEdit is nil when an edit of target may proceed: always in
// workspace mode, and in explicit mode only when target is admitted both
// under the name the call used and under the file it resolves to. real is
// where target lands once symlinks are followed (the same as target when
// none is involved), so a link inside an admitted directory cannot carry
// an edit to a file nobody admitted. named is how the call spelled the
// path, for the message.
func (p *Policy) CheckEdit(root, target, real, named string) error {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.mode != Explicit {
		return nil
	}
	target, real = filepath.Clean(target), filepath.Clean(real)
	if coveredBy(root, p.edit, target) && coveredBy(root, p.edit, real) {
		return nil
	}
	attached := coveredBy(root, p.attach, target) || coveredBy(root, p.attach, real)
	shown := named
	if shown == "" {
		shown = key(root, target, false)
	}
	return &Denied{Path: shown, Attached: attached}
}

// Describe is the report /admit prints: the mode, then each list with a
// directory entry resolved to its file count and a few names.
func (p *Policy) Describe(root string) string {
	if p == nil {
		return "file mode: workspace"
	}
	p.mu.RLock()
	mode := p.mode
	edit, attach := append([]string(nil), p.edit...), append([]string(nil), p.attach...)
	p.mu.RUnlock()
	var b strings.Builder
	if mode == Explicit {
		b.WriteString("file mode: explicit (edits only to admitted files)")
	} else {
		b.WriteString("file mode: workspace (any file under the working directory may be edited; /admit mode explicit narrows that)")
	}
	section := func(title string, list []string) {
		if len(list) == 0 {
			return
		}
		b.WriteString("\n" + title + ":")
		for _, e := range list {
			b.WriteString("\n  " + Summary(root, e))
		}
	}
	section("admitted for editing", edit)
	section("attached read-only", attach)
	if mode == Explicit && len(edit) == 0 {
		b.WriteString("\nnothing is admitted yet: /admit <path> admits a file or directory for editing")
	}
	return b.String()
}

// Lines is the compact one-line-per-entry form the status chips use.
func (p *Policy) Lines() (edit, attach []string) {
	if p == nil {
		return nil, nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]string(nil), p.edit...), append([]string(nil), p.attach...)
}

// Bounds on a directory's resolved file set: a tree is walked until it
// has this many files or entries, and only a few names are shown.
const (
	maxFiles  = 2000
	maxVisits = 20000
	maxShown  = 8
)

// Summary describes one entry: a file is its path; a directory is its
// path, its file count (the walk skips .git and what the project's
// ignore files exclude, the way the file tools do) and a bounded sample.
func Summary(root, entry string) string {
	if !isDirEntry(entry) {
		return entry
	}
	dir := abs(root, entry)
	files, truncated := Files(root, dir)
	if len(files) == 0 {
		return entry + " (no files yet)"
	}
	count := fmt.Sprint(len(files))
	if truncated {
		count = fmt.Sprintf("more than %d", len(files))
	}
	n := len(files)
	if n > maxShown {
		n = maxShown
	}
	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = filepath.ToSlash(relOr(dir, files[i]))
	}
	more := ""
	if len(files) > n {
		more = ", ..."
	}
	return fmt.Sprintf("%s (%s files: %s%s)", entry, count, strings.Join(names, ", "), more)
}

func relOr(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}

// Files lists the files under dir that an admission of dir covers today,
// sorted, up to a bound; truncated says the bound was hit. Directories
// the ignore files exclude and .git are skipped.
func Files(root, dir string) (files []string, truncated bool) {
	visits := 0
	m := ignore.For(rootFor(root, dir))
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if visits++; visits > maxVisits || len(files) >= maxFiles {
			truncated = true
			return filepath.SkipAll
		}
		if path == dir {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if hit, _ := m.Excluded(path, true); hit {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if hit, _ := m.Excluded(path, false); hit {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, truncated
}

// rootFor is the root whose ignore files govern dir.
func rootFor(root, dir string) string {
	if root != "" {
		if rel, err := filepath.Rel(root, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return root
		}
	}
	return dir
}

// Prompt is what the model is told when the mode is explicit: the files
// it may edit and the ones that are context only. Empty in workspace
// mode, so a session that does not use admission pays nothing.
func (p *Policy) Prompt(root string) string {
	if !p.Explicit() {
		return ""
	}
	edit, attach := p.Lines()
	var b strings.Builder
	b.WriteString("\n\n# File admission (explicit)\nThis session restricts edits to files the user admitted. You may read any file under the working directory as usual, but write_file, edit_file, ast_edit, notebook_edit and any shell command that changes a file are refused for every other path. A refusal names the `/admit` command that would allow it: tell the user and wait; do not try another tool to get around it.\n")
	if len(edit) == 0 {
		b.WriteString("\nAdmitted for editing: nothing yet. Say so if the task needs a change, and ask the user to admit the files.\n")
	} else {
		b.WriteString("\nAdmitted for editing:\n")
		for _, e := range edit {
			b.WriteString("- " + Summary(root, e) + "\n")
		}
	}
	if len(attach) > 0 {
		b.WriteString("\nAttached read-only context (do not edit):\n")
		for _, e := range attach {
			b.WriteString("- " + Summary(root, e) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
