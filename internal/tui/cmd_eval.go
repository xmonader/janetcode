package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/eval"
)

// evalKeepMsg carries the result of /eval keep back from the goroutine
// that ran it.
type evalKeepMsg struct {
	res cmdresult.Result
	err error
}

// evalCommand implements /eval keep [name] [--base <rev>]
// [--self-contained] [--check <command>] through eval.KeepCommand: it
// turns this session into a regression eval task under .janetcode/evals.
// The dispatcher already blocks it while a turn is running (it is
// idle-only, see slash.idleOnly), since it reads the session log. It runs
// off the UI goroutine because KeepCommand runs the kept check once at
// the task's start state, which takes as long as the check does.
func (m *model) evalCommand(arg string) tea.Cmd {
	ctx := m.startSideWork("keeping the eval")
	opts, send := eval.KeepOptions{Root: m.opts.Cwd, Session: m.opts.Session}, m.send
	go func() {
		res, err := eval.KeepCommand(ctx, opts, arg)
		send(evalKeepMsg{res: res, err: err})
	}()
	m.refresh(false)
	return m.spinner.Tick
}

// finishEvalKeep shows what /eval keep wrote and any warnings.
func (m *model) finishEvalKeep(msg evalKeepMsg) tea.Cmd {
	m.showResult(msg.res, msg.err)
	return m.endSideWork(msg.err == nil)
}
