package tool

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/admit"
)

// Admission is the session's file admission policy (see package admit).
// It is created with the workspace and shared by every tool that holds it,
// so the file tools, the agent's gate and the front ends read one list.
func (w *Workspace) Admission() *admit.Policy {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.admission == nil {
		w.admission = admit.New()
	}
	return w.admission
}

// Admit resolves path the way a file tool would (so confinement, symlinks
// and the ignore files apply first) and admits it for editing, or, with
// attach, as read-only context. It returns the path as stored.
func (w *Workspace) Admit(path string, attach bool) (string, error) {
	if w == nil {
		return "", fmt.Errorf("this session has no workspace to admit files in")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is empty")
	}
	abs, err := resolveOn(w, w.Root(), path)
	if err != nil {
		return "", err
	}
	if attach {
		w.Admission().AddAttach(w.Root(), abs)
	} else {
		w.Admission().AddEdit(w.Root(), abs)
	}
	return displayPath(w.Root(), abs), nil
}

// Unadmit drops path from the admission lists.
func (w *Workspace) Unadmit(path string) (bool, error) {
	if w == nil {
		return false, fmt.Errorf("this session has no workspace")
	}
	abs, err := resolveOn(w, w.Root(), strings.TrimSpace(path))
	if err != nil {
		return false, err
	}
	return w.Admission().Remove(w.Root(), abs), nil
}

// admitErr is the explicit-mode refusal for an edit of abs, or nil. It
// judges the path as named and, when a symlink is involved, the file it
// leads to as well.
func admitErr(ws *Workspace, abs, named string) error {
	if ws == nil {
		return nil
	}
	pol := ws.Admission()
	if !pol.Explicit() {
		return nil
	}
	real := abs
	if ws.Local() {
		if r, ok := realUnder(ws.Roots(), abs); ok {
			real = r.path
		}
	}
	return pol.CheckEdit(ws.Root(), abs, real, named)
}

// resolveWriteOn is resolveOn for a path the call will change: after the
// confinement and ignore checks, the file must be admitted when the
// session's file mode is explicit.
func resolveWriteOn(ws *Workspace, root, p string) (string, error) {
	abs, err := resolveOn(ws, root, p)
	if err != nil {
		return "", err
	}
	if err := admitErr(ws, abs, p); err != nil {
		return "", err
	}
	return abs, nil
}

// AdmitDenial is the explicit-mode refusal for a call to t, or "": a file
// tool is judged by the paths it says it will write. The agent asks
// before it asks the user to approve, so a call that cannot run is not
// put to them.
func AdmitDenial(t Tool, input []byte) string {
	tc, ok := t.(Toucher)
	if !ok {
		return ""
	}
	_, err := tc.Touches(input)
	if err == nil {
		return ""
	}
	var d *admit.Denied
	if errors.As(err, &d) {
		return err.Error()
	}
	return ""
}

// AdmitCheck is the explicit-mode verdict on an edit of abs by something
// that is not a file tool (a shell command's write target): nil when it
// may proceed, or when abs lies outside every root, where confinement and
// the sandbox, not admission, govern it. named is how the caller spells
// the path in the refusal.
func (w *Workspace) AdmitCheck(abs, named string) error {
	if w == nil || !w.Admission().Explicit() || !w.Local() {
		return nil
	}
	if rootContaining(w.Roots(), abs) == "" {
		return nil
	}
	return admitErr(w, abs, named)
}
