package webui

import (
	"context"

	"github.com/xmonader/janetcode/internal/eval"
)

// evalKeep implements /eval keep [name] [--base <rev>] [--self-contained]
// [--check <command>] through eval.KeepCommand: it turns this session
// into a regression eval task under .janetcode/evals. The dispatcher
// already blocks it while a turn is running (it is idle-only), since it
// reads the session log; it runs busy because KeepCommand runs the kept
// check once at the task's start state.
func (s *server) evalKeep(arg string) {
	s.runBusy("keeping the eval", func(ctx context.Context) {
		s.showResult(eval.KeepCommand(ctx, eval.KeepOptions{Root: s.cwd(), Session: s.opts.Session}, arg))
	})
}
