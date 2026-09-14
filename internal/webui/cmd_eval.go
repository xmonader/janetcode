package webui

import "github.com/xmonader/janetcode/internal/eval"

// evalKeep implements /eval keep [name] [--base <rev>] [--self-contained]
// [--check <command>] through eval.KeepCommand: it turns this session
// into a regression eval task under .janetcode/evals. The dispatcher
// already blocks it while a turn is running (it is idle-only), since it
// reads the session log.
func (s *server) evalKeep(arg string) {
	s.showResult(eval.KeepCommand(eval.KeepOptions{Root: s.opts.Cwd, Session: s.opts.Session}, arg))
}
