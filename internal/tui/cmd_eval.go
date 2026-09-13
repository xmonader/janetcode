package tui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// evalCommand implements /eval keep [name] [--base <rev>]
// [--self-contained] [--check <command>]: it turns this session into a
// regression eval task under .janetcode/evals. The dispatcher already
// blocks it while a turn is running (it is idle-only, see
// slash.idleOnly), since it reads the session log.
func (m *model) evalCommand(arg string) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if verb != "keep" {
		m.transcript.errorf("usage: /eval keep [name] [--base <rev>] [--self-contained] [--check <command>]")
		return
	}
	name, check, base, selfContained, err := eval.ParseKeepArgs(rest)
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	path, task, warnings, err := eval.Keep(eval.KeepOptions{Root: m.opts.Cwd, Session: m.opts.Session, Name: name, Check: check, Base: base, SelfContained: selfContained})
	if err != nil {
		m.transcript.errorf("eval keep: %v", err)
		return
	}
	m.note(shortPath(path) + " · " + textfmt.Plural(len(task.Prompts), "prompt") + " · run it with: janetcode --eval .janetcode/evals")
	for _, w := range warnings {
		m.transcript.tip(w)
	}
}
