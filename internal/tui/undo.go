package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// undoChange reverts one change from /changes on its own.
func (m *model) undoChange(n int) {
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before undoing")
		return
	}
	done, err := m.opts.Agent.UndoChange(n)
	if err != nil {
		m.transcript.errorf("undo #%d: %v", n, err)
		return
	}
	verb, path, _ := strings.Cut(done, " ")
	if i := strings.LastIndex(done, " "); i > 0 {
		verb, path = done[:i], done[i+1:]
	}
	m.transcript.info(fmt.Sprintf("%s#%d: %s %s", noteUndo, n, verb, m.relPath(path)))
	m.opts.Agent.AddContext(fmt.Sprintf("I undid change #%d (%s) on my own; later changes to the file stayed.", n, m.relPath(path)))
}

// changesCommand lists the session's changes, or one change's hunks.
func (m *model) changesCommand(args []string) {
	if len(args) == 1 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			m.transcript.errorf("usage: /changes [n]")
			return
		}
		c, err := m.opts.Agent.Change(n)
		if err != nil {
			m.transcript.errorf("%v", err)
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "#%d  %s  %s", c.Seq, c.Tool, m.relPath(c.Path))
		if c.Intent != "" {
			sb.WriteString("  · " + c.Intent)
		}
		if c.Undone {
			sb.WriteString("  (undone)")
		}
		sb.WriteString("\n")
		for _, h := range c.Hunks() {
			sb.WriteString(h.String())
		}
		m.transcript.code(strings.TrimRight(sb.String(), "\n"))
		return
	}
	groups := m.opts.Agent.ChangeGroups()
	if len(groups) == 0 {
		m.transcript.info("no file changes in this session yet")
		return
	}
	var sb strings.Builder
	for _, g := range groups {
		fmt.Fprintf(&sb, "turn %d", g.Turn)
		if g.Intent != "" {
			fmt.Fprintf(&sb, " · %s", g.Intent)
		}
		sb.WriteString("\n")
		for _, c := range g.Changes {
			add, del := c.Stats()
			fmt.Fprintf(&sb, "  #%-3d %-10s %s  +%d -%d", c.Seq, c.Tool, m.relPath(c.Path), add, del)
			if c.Undone {
				sb.WriteString("  (undone)")
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("/changes <n> shows the hunks; /undo <n> reverts one change; /blame <path> attributes lines")
	m.transcript.code(sb.String())
}

// blameCommand attributes each line of a file to the change that wrote it.
func (m *model) blameCommand(path string) {
	if path == "" {
		m.transcript.errorf("usage: /blame <path>")
		return
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(m.opts.Cwd, path)
	}
	lines, note, err := m.opts.Agent.Blame(abs)
	if err != nil {
		// This session never changed the file; the commit(s) that did may
		// still carry proof of who wrote each line and why (see
		// internal/proof), so try that before giving up.
		if errors.Is(err, agent.ErrNoChange) {
			if text, cerr := m.blameCommitted(abs); cerr == nil {
				m.transcript.code(text)
				return
			}
		}
		m.transcript.errorf("%v", err)
		return
	}
	var sb strings.Builder
	if note != "" {
		sb.WriteString("note: " + note + "\n")
	}
	for i, l := range lines {
		who := "    ·"
		if l.Seq > 0 {
			who = fmt.Sprintf("#%-4d", l.Seq)
		}
		fmt.Fprintf(&sb, "%s %4d │ %s\n", who, i+1, l.Text)
	}
	m.transcript.code(strings.TrimRight(sb.String(), "\n"))
}

// blameCommitted attributes a file's lines from committed history, via
// git blame plus whatever proof notes the commits that wrote them carry
// (see internal/proof.BlameCommitted). Used when this session never
// changed the file itself.
func (m *model) blameCommitted(abs string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	lines, err := proof.BlameCommitted(ctx, m.opts.Cwd, abs)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for i, l := range lines {
		sha := proof.Short(l.Commit)
		fmt.Fprintf(&sb, "%s %4d │ %s", sha, i+1, l.Text)
		if l.Intent != "" {
			fmt.Fprintf(&sb, "  · %s", l.Intent)
		}
		if l.Verdict != "" {
			fmt.Fprintf(&sb, " (%s)", l.Verdict)
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// relPath shortens an absolute path under the working directory.
func (m *model) relPath(path string) string {
	if r, err := filepath.Rel(m.opts.Cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		path = r
	}
	return shortPath(path)
}

// undoCommand handles /undo, /undo preview and /redo.
func (m *model) undoCommand(cmd string, preview bool) {
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before %s", strings.TrimPrefix(cmd, "/")+"ing")
		return
	}
	rel := func(path string) string {
		if r, err := filepath.Rel(m.opts.Cwd, path); err == nil && !strings.HasPrefix(r, "..") {
			path = r
		}
		return shortPath(path)
	}
	if preview {
		lines, err := m.opts.Agent.UndoPreview()
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.transcript.errorf("nothing to undo: no turn has changed anything yet")
			return
		}
		if err != nil {
			m.transcript.errorf("undo preview: %v", err)
			return
		}
		if len(lines) == 0 {
			m.note("/undo would change nothing (the tree already matches the snapshot)")
			return
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "/undo would revert %s:\n", textfmt.Plural(len(lines), "file"))
		for _, l := range lines {
			verb, path, _ := strings.Cut(l, " ")
			fmt.Fprintf(&sb, "  %-8s %s\n", verb, rel(path))
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
		return
	}
	var (
		done []string
		err  error
		mark = "↶"
	)
	if cmd == "/redo" {
		mark = "↷"
		done, err = m.opts.Agent.Redo()
		if errors.Is(err, agent.ErrNothingToRedo) {
			m.transcript.errorf("nothing to redo: /redo only reapplies an /undo from before the next change")
			return
		}
	} else {
		done, err = m.opts.Agent.Undo()
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.transcript.errorf("nothing to undo: no turn has changed anything yet")
			return
		}
	}
	for _, d := range done {
		verb, path, _ := strings.Cut(d, " ")
		m.transcript.info(mark + " " + verb + " " + rel(path))
	}
	if err != nil {
		m.transcript.errorf("%s incomplete: %v", strings.TrimPrefix(cmd, "/"), err)
	}
	if len(done) == 0 {
		return
	}
	if cmd == "/redo" {
		m.opts.Agent.AddContext("I reapplied the file changes I had undone:\n" + strings.Join(done, "\n"))
		m.status = fmt.Sprintf("redid %s · /undo reverts them again", textfmt.Plural(len(done), "file change"))
		return
	}
	if n := m.opts.Agent.LastUndoRemoved(); n > 0 {
		// The turn's messages left the context with its files; the
		// agent put one note in their place, so nothing more to add.
		m.note(fmt.Sprintf("the undone turn's %s removed from the model's context as well", textfmt.Plural(n, "message")))
	} else {
		m.opts.Agent.AddContext("I reverted the file changes from your last edits:\n" + strings.Join(done, "\n") + "\nDo not redo them unless I ask.")
	}
	m.status = fmt.Sprintf("undid %s · %s can still be undone · /redo reapplies", textfmt.Plural(len(done), "file change"), textfmt.Plural(len(m.opts.Agent.Checkpoints()), "more turn"))
}
