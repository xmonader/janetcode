package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// undoChange reverts one change from /changes on its own.
func (m *model) undoChange(n int) {
	if m.turn.running {
		m.transcript.errorf("wait for the current turn to finish before undoing")
		return
	}
	done, err := m.opts.Agent.UndoChange(n)
	if err != nil {
		m.transcript.errorf("undo #%d: %v", n, err)
		return
	}
	// done is "<verb> <path>", the path already relative. The path comes
	// from the change itself: splitting done at a space cut a name with a
	// space in it, so "my notes.txt" reached the model as "notes.txt".
	path := ""
	if c, err := m.opts.Agent.Change(n); err == nil {
		path = m.relPath(c.Path)
	}
	m.transcript.info(fmt.Sprintf("%s#%d: %s", noteUndo, n, done))
	m.opts.Agent.AddHarnessContext(fmt.Sprintf("I undid change #%d (%s) on my own; later changes to the file stayed.", n, path))
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
		m.opts.Agent.MarkChangeReviewed(n)
		m.transcript.code(agent.FormatChange(c, m.opts.Cwd))
		return
	}
	groups := m.opts.Agent.ChangeGroups()
	if len(groups) == 0 {
		m.transcript.info("no file changes in this session yet")
		return
	}
	out := agent.FormatChangeGroups(groups, m.opts.Cwd, m.opts.Agent.RestorableTurns())
	if line := m.opts.Agent.DebtLine(m.opts.Cwd); line != "" {
		out += "\n" + line
	}
	m.transcript.code(out + "\n/changes <n> shows the hunks; /tour reviews a turn riskiest first; /undo <n> reverts one change; /undo turn <n> puts the tree and the conversation back to before a turn; /blame <path> attributes lines")
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
	ctx, cancel := context.WithTimeout(m.life, 15*time.Second)
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
	if m.turn.running {
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
		lines, err := m.opts.Agent.UndoPreview(m.life)
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.note("nothing to undo: no turn has changed anything yet")
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
		done, err = m.opts.Agent.Redo(m.life)
		if errors.Is(err, agent.ErrNothingToRedo) {
			m.note("nothing to redo: /redo only reapplies an /undo from before the next change")
			return
		}
	} else {
		spread := m.undoSpread()
		done, err = m.opts.Agent.Undo(m.life)
		if err == nil && len(done) > 0 {
			defer m.observeUndo(spread)
		}
		if errors.Is(err, agent.ErrNothingToUndo) {
			m.note("nothing to undo: no turn has changed anything yet")
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
		m.opts.Agent.AddHarnessContext("I reapplied the file changes I had undone:\n" + strings.Join(done, "\n"))
		m.status = fmt.Sprintf("redid %s · /undo reverts them again", textfmt.Plural(len(done), "file change"))
		return
	}
	if n := m.opts.Agent.LastUndoRemoved(); n > 0 {
		// The turn's messages left the context with its files; the
		// agent put one note in their place, so nothing more to add.
		m.note(fmt.Sprintf("the undone turn's %s removed from the model's context as well", textfmt.Plural(n, "message")))
	} else {
		m.opts.Agent.AddHarnessContext("I reverted the file changes from your last edits:\n" + strings.Join(done, "\n") + "\nDo not redo them unless I ask.")
	}
	more := "no earlier turn to undo"
	if n := len(m.opts.Agent.Checkpoints()); n > 0 {
		more = textfmt.Plural(n, "more turn") + " can still be undone"
	}
	m.status = fmt.Sprintf("undid %s · %s · /redo reapplies", textfmt.Plural(len(done), "file change"), more)
}

// undoAboutMsg carries the result of "/undo about <description>" back
// from the goroutine that ran it, the same way btwMsg and doctorMsg do
// for their own side calls.
type undoAboutMsg struct {
	res cmdresult.Result
	err error
}

// undoAboutCommand previews which of this session's changes match
// description, without touching anything. The match can call a model, so
// it runs off the UI goroutine — as a turn does, with the session marked
// running, so no turn or cron round starts on the agent meanwhile and
// what it types is queued (see startSideWork).
func (m *model) undoAboutCommand(description string) tea.Cmd {
	description = strings.TrimSpace(description)
	if description == "" {
		m.transcript.errorf("%v", agent.ErrUndoAboutUsage)
		return nil
	}
	ctx := m.startSideWork(fmt.Sprintf("matching %q against this session's changes", description))
	ag, send, cwd := m.opts.Agent, m.send, m.opts.Cwd
	go func() {
		res, err := ag.UndoAbout(ctx, cwd, description)
		send(undoAboutMsg{res: res, err: err})
	}()
	m.refresh(false)
	return m.spinner.Tick
}

// startSideWork marks the session running for work that is not a model
// turn but uses the agent's history or undo stack off the UI goroutine
// (/undo about, /undo apply): prompts typed meanwhile are queued, cron
// rounds skipped, idle-only commands refused, and esc cancels the
// returned context. endSideWork undoes it when the result arrives. phase
// is what the status line says while the work runs; it goes with the
// work, where a status note set here would outlive it.
func (m *model) startSideWork(phase string) context.Context {
	m.startTurn(phase)
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	return ctx
}

// endSideWork ends what startSideWork began and sends the first queued
// prompt, the way the end of a turn does. ok is whether the work
// succeeded, for the terminal's command-zone mark.
func (m *model) endSideWork(ok bool) tea.Cmd {
	m.turn.finish()
	m.turn.setPhase("")
	m.term.turnEnded(ok, m.status)
	next, queued := m.turn.dequeue()
	if !queued {
		return nil
	}
	return m.submit(next)
}

// finishUndoAbout shows the preview and, when it found anything to
// revert, points at /undo apply.
func (m *model) finishUndoAbout(msg undoAboutMsg) tea.Cmd {
	m.showResult(msg.res, msg.err)
	return m.endSideWork(msg.err == nil)
}

// undoTurnCommand previews putting the work tree back to how it stood
// before turn n — and, unless filesOnly, rewinding the conversation with
// it — without touching anything. It reads git, so it runs off the UI
// goroutine the way /undo about does; "/undo apply" carries it out.
func (m *model) undoTurnCommand(n int, filesOnly bool) tea.Cmd {
	ctx := m.startSideWork(fmt.Sprintf("listing what restoring the tree to before turn %d would change", n))
	ag, send, cwd := m.opts.Agent, m.send, m.opts.Cwd
	go func() {
		res, err := ag.UndoToTurn(ctx, cwd, n, filesOnly)
		send(undoAboutMsg{res: res, err: err})
	}()
	m.refresh(false)
	return m.spinner.Tick
}

// undoApplyMsg carries the result of "/undo apply".
type undoApplyMsg struct {
	rep agent.UndoReport
	res cmdresult.Result
	err error
}

// undoApplyCommand reverts the selection from the last /undo about. It
// runs off the UI goroutine because it can run the project's verify
// checks, which may take a while. force is --force: proceed even though
// the selection has a mixed change or a coupling warning.
func (m *model) undoApplyCommand(force bool) tea.Cmd {
	ctx := m.startSideWork("applying the previewed undo")
	ag, send := m.opts.Agent, m.send
	go func() {
		rep, res, err := ag.UndoApply(ctx, force)
		send(undoApplyMsg{rep: rep, res: res, err: err})
	}()
	m.refresh(false)
	return m.spinner.Tick
}

// finishUndoApply shows what was reverted and tells the model, so it
// does not go on believing changes are still there.
func (m *model) finishUndoApply(msg undoApplyMsg) tea.Cmd {
	m.showResult(msg.res, msg.err)
	if msg.err == nil {
		// Before endSideWork, which may start a queued turn: the note has
		// to be in the history that turn sends.
		m.opts.Agent.TellModelUndone(msg.rep)
	}
	return m.endSideWork(msg.err == nil)
}
