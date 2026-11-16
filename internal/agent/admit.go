package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/admit"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// File admission (package admit) is enforced three ways, because a file
// can change three ways:
//
//   - the file tools refuse an edit of a path that is not admitted, in
//     the tool itself (tool.resolveWriteOn), so a script, a fork or ACP
//     cannot skip it;
//   - a shell command is read before it runs for the files it writes
//     (shell.WriteTargets), and refused when one is not admitted;
//   - a command whose writes cannot be read from its text (`make gen`, a
//     script) is watched: the work tree is read around the call, and a
//     file it changed that is not admitted is put back, or named when it
//     cannot be.
//
// An editing subagent works in a scratch checkout, so what it changes
// reaches the project only through merge-back, which refuses a file that
// is not admitted (mergeRefused).

// admitPolicy is the session's file admission policy, nil without a
// workspace.
func (a *Agent) admitPolicy() *admit.Policy {
	if a.Workspace == nil {
		return nil
	}
	return a.Workspace.Admission()
}

// admitGate is the explicit-mode refusal for a mutating call, or "".
func (a *Agent) admitGate(t tool.Tool, input []byte) string {
	if !a.admitPolicy().Explicit() {
		return ""
	}
	if why := tool.AdmitDenial(t, input); why != "" {
		return why
	}
	switch t.Name() {
	case "bash", "monitor":
		return a.admitCommandDenial(shell.CommandOf(string(input)))
	}
	return ""
}

// admitCommandDenial refuses a shell command that visibly writes a file
// that is not admitted.
func (a *Agent) admitCommandDenial(command string) string {
	if command == "" {
		return ""
	}
	root := a.Workspace.Root()
	for _, p := range shell.WriteTargets(command, root) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		if err := a.Workspace.AdmitCheck(p, displayRel(root, p)); err != nil {
			return err.Error() + " (the command writes it)"
		}
	}
	return ""
}

func displayRel(root, p string) string {
	if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	return p
}

// admitSystem is the system prompt's list of admitted files.
func (a *Agent) admitSystem() string {
	if a.Workspace == nil {
		return ""
	}
	return a.admitPolicy().Prompt(a.Workspace.Root())
}

// admitSnap is the dirty files' digests before a command whose writes
// are watched.
type admitSnap struct {
	repoOwner *Agent
	before    bashAuditState
}

// admitBefore reads the work tree before a shell call, when the session
// is in explicit mode and the call is one that can write opaquely.
func (a *Agent) admitBefore(ctx context.Context, u llm.Block) *admitSnap {
	if u.Name != "bash" && u.Name != "term" {
		return nil
	}
	if !a.admitPolicy().Explicit() {
		return nil
	}
	owner := a
	if a.UndoOwner != nil {
		owner = a.UndoOwner
	}
	repo := owner.userRepo()
	if repo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, bashAuditTimeout)
	defer cancel()
	paths, err := repo.Dirty(ctx)
	if err != nil || len(paths) > bashAuditMax {
		return nil
	}
	st := bashAuditState{}
	for _, p := range paths {
		st[p] = fileDigest(p)
	}
	return &admitSnap{repoOwner: owner, before: st}
}

// admitAfter puts back every file the call changed that is not admitted
// and returns the note the model reads, or "". A file that was clean
// before is restored from the index (or removed, when the call created
// it); one that already held uncommitted edits cannot be restored, and
// is named.
func (a *Agent) admitAfter(ctx context.Context, snap *admitSnap) string {
	if snap == nil {
		return ""
	}
	repo := snap.repoOwner.userRepo()
	if repo == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bashAuditTimeout)
	defer cancel()
	now, err := repo.Dirty(ctx)
	if err != nil {
		return "\n\n[file mode explicit: could not check what this command changed (" + err.Error() + "); run git status]"
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range now {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for p := range snap.before {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	root := a.Workspace.Root()
	var restored, stuck []string
	for _, p := range paths {
		prev, wasDirty := snap.before[p]
		after := fileDigest(p)
		if wasDirty && prev == after {
			continue
		}
		name := displayRel(root, p)
		if a.Workspace.AdmitCheck(p, name) == nil {
			continue
		}
		if wasDirty {
			if prev != after {
				stuck = append(stuck, name)
			}
			continue
		}
		data, inIndex, err := repo.IndexContent(ctx, p)
		if err != nil {
			stuck = append(stuck, name)
			continue
		}
		if inIndex {
			if err := writeBack(p, data); err != nil {
				stuck = append(stuck, name)
				continue
			}
		} else if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			stuck = append(stuck, name)
			continue
		}
		restored = append(restored, name)
	}
	if len(restored) == 0 && len(stuck) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[file mode explicit: this command changed files that are not admitted for editing.")
	if len(restored) > 0 {
		b.WriteString(" Put back: " + strings.Join(restored, ", ") + ".")
	}
	if len(stuck) > 0 {
		b.WriteString(" Could not put back (they held uncommitted edits before the command): " + strings.Join(stuck, ", ") + "; tell the user.")
	}
	b.WriteString(" The user can admit a path with `/admit <path>` (headless: --admit <path>); do not run the command again until they have.]")
	return b.String()
}

func writeBack(p string, data []byte) error {
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(p, data, mode)
}

// mergeNotAdmitted is the merge-back refusal for paths a subagent's
// checkout changed that the session has not admitted for editing, or "".
func (t *Task) mergeNotAdmitted(root string, paths []string) string {
	ws := t.Parent.Workspace
	if ws == nil || !ws.Admission().Explicit() {
		return ""
	}
	for _, rel := range paths {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := ws.AdmitCheck(abs, rel); err != nil {
			return "file mode is explicit and " + err.Error()
		}
	}
	return ""
}

// admitPersist stores the policy in the session record.
func admitPersist(ws *tool.Workspace, sess *session.Session) {
	if sess != nil && ws != nil {
		_ = sess.SetAdmission(ws.Admission().Snapshot())
	}
}

// Admit applies a /admit argument (the same grammar in the terminal and
// the web UI) and returns the report to show:
//
//	/admit                       the mode and what is admitted
//	/admit <path>...             admit files or directories for editing
//	/admit mode explicit|workspace
//	/admit remove <path>...      drop paths from both lists
//	/admit clear                 drop every path
//
// The change is persisted with the session so a resume keeps it.
func Admit(ws *tool.Workspace, sess *session.Session, arg string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("this session has no workspace to admit files in")
	}
	words := shell.Words(arg)
	pol := ws.Admission()
	if len(words) == 0 || (len(words) == 1 && words[0] == "status") {
		return pol.Describe(ws.Root()), nil
	}
	switch words[0] {
	case "mode":
		if len(words) != 2 {
			return "", fmt.Errorf("usage: /admit mode explicit|workspace")
		}
		m, err := admit.ParseMode(words[1])
		if err != nil {
			return "", err
		}
		pol.SetMode(m)
		admitPersist(ws, sess)
		return pol.Describe(ws.Root()), nil
	case "clear":
		pol.Clear()
		admitPersist(ws, sess)
		return "admission lists cleared\n" + pol.Describe(ws.Root()), nil
	case "remove", "rm":
		if len(words) < 2 {
			return "", fmt.Errorf("usage: /admit remove <path>...")
		}
		var gone []string
		for _, w := range words[1:] {
			ok, err := ws.Unadmit(w)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("%s is not on the admission lists", w)
			}
			gone = append(gone, w)
		}
		admitPersist(ws, sess)
		return "removed " + strings.Join(gone, ", ") + "\n" + pol.Describe(ws.Root()), nil
	}
	return admitPaths(ws, sess, words, false)
}

// Attach applies a /attach argument: files or directories added as
// read-only context. Nothing is edited through them until they are
// admitted.
func Attach(ws *tool.Workspace, sess *session.Session, arg string) (string, error) {
	if ws == nil {
		return "", fmt.Errorf("this session has no workspace to attach files in")
	}
	words := shell.Words(arg)
	if len(words) == 0 {
		return ws.Admission().Describe(ws.Root()), nil
	}
	return admitPaths(ws, sess, words, true)
}

func admitPaths(ws *tool.Workspace, sess *session.Session, paths []string, attach bool) (string, error) {
	pol := ws.Admission()
	for _, p := range paths {
		if _, err := ws.Admit(p, attach); err != nil {
			return "", err
		}
	}
	admitPersist(ws, sess)
	verb := "admitted for editing"
	if attach {
		verb = "attached read-only"
	}
	out := verb + ": " + strings.Join(paths, ", ") + "\n" + pol.Describe(ws.Root())
	return out, nil
}

// NoteMentions attaches the files a prompt's @mentions pulled in as
// read-only context while the session's file mode is explicit, so the
// model's list of what it was sent matches what it was sent.
func NoteMentions(ws *tool.Workspace, sess *session.Session, paths []string) {
	if ws == nil || !ws.Admission().Explicit() {
		return
	}
	changed := false
	for _, p := range paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(ws.Root(), p)
		}
		if _, err := ws.Admit(abs, true); err == nil {
			changed = true
		}
	}
	if changed {
		admitPersist(ws, sess)
	}
}
