package webui

import (
	"context"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/gitflow"
)

// gitCommand is /commit, /pr, /pr-comments and /issue, as the terminal
// runs them: a draft is put in the composer ("/commit -m …", "/pr create
// …") for the user to edit and send, so nothing is committed or pushed
// until they do.
func (s *server) gitCommand(name, arg string) {
	var ask gitflow.Asker
	var attach gitflow.AttachFunc
	cwd := s.cwd()
	if ag := s.opts.Agent; ag != nil {
		ask, attach = ag, ag.ProofAttacher(cwd)
	}
	s.notice(gitflow.Status(name, arg))
	go func() {
		ctx, cancel := context.WithTimeout(s.life, 2*time.Minute)
		defer cancel()
		s.showGitflow(name, gitflow.Route(ctx, ask, attach, cwd, name, arg))
	}()
}

func (s *server) showGitflow(name string, r gitflow.Result) {
	if r.Err != nil {
		s.errf("%v", r.Err)
	}
	if r.Info != "" {
		s.sheet(strings.TrimPrefix(name, "/"), r.Info)
	}
	if r.Shared != "" && s.opts.Agent != nil {
		s.opts.Agent.AddContext(r.Shared)
	}
	if r.Compose != "" {
		s.emit(envelope{Type: "compose", Compose: r.Compose})
	}
	if r.Note != "" {
		s.notice(r.Note)
	}
}

// securityReview is /security-review: two read-only subagents review the
// diff with a security brief and one ranked report comes back.
func (s *server) securityReview(arg string) {
	task := s.taskTool()
	if task == nil {
		s.notice("no task tool")
		return
	}
	diff, err := agent.ReviewDiff(s.life, s.cwd())
	if err != nil {
		s.errf("security review: %v", err)
		return
	}
	if strings.TrimSpace(diff) == "" && arg == "" {
		s.notice("nothing to review (working tree clean)")
		return
	}
	go func() {
		report := agent.RunSecurityReview(s.life, task, arg, diff)
		s.showGitflow("/security-review", gitflow.Result{
			Info:   report,
			Shared: agent.SecurityReviewShared(report),
			Note:   "security review done · the findings are shared with the model",
		})
	}()
	s.notice("started security review · two subagents · /agents watches them · the ranked report follows")
}
