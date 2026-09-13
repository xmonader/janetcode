package webui

import (
	"strings"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// evalKeep implements /eval keep [name] [--base <rev>] [--self-contained]
// [--check <command>]: it turns this session into a regression eval task
// under .janetcode/evals. The dispatcher already blocks it while a turn
// is running (it is idle-only), since it reads the session log.
func (s *server) evalKeep(arg string) {
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	if verb != "keep" {
		s.errf("usage: /eval keep [name] [--base <rev>] [--self-contained] [--check <command>]")
		return
	}
	name, check, base, selfContained, err := eval.ParseKeepArgs(rest)
	if err != nil {
		s.errf("%v", err)
		return
	}
	path, task, warnings, err := eval.Keep(eval.KeepOptions{Root: s.opts.Cwd, Session: s.opts.Session, Name: name, Check: check, Base: base, SelfContained: selfContained})
	if err != nil {
		s.errf("eval keep: %v", err)
		return
	}
	s.notice(path + " · " + textfmt.Plural(len(task.Prompts), "prompt") + " · run it with: janetcode --eval .janetcode/evals")
	for _, w := range warnings {
		s.notice(w)
	}
}
