package tui

import "github.com/xmonader/janetcode/internal/eval"

// evalCommand implements /eval keep [name] [--base <rev>]
// [--self-contained] [--check <command>] through eval.KeepCommand: it
// turns this session into a regression eval task under .janetcode/evals.
// The dispatcher already blocks it while a turn is running (it is
// idle-only, see slash.idleOnly), since it reads the session log.
func (m *model) evalCommand(arg string) {
	m.showResult(eval.KeepCommand(eval.KeepOptions{Root: m.opts.Cwd, Session: m.opts.Session}, arg))
}
