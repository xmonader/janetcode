package tui

import (
	"context"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/gitflow"
)

// gitflowMsg is the answer of /commit, /pr, /pr-comments, /issue or
// /security-review, from the goroutine that did the work.
type gitflowMsg struct{ gitflow.Result }

func (m *model) handleGitflow(msg gitflowMsg) {
	if msg.Err != nil {
		m.transcript.errorf("%v", msg.Err)
	}
	if msg.Info != "" {
		m.transcript.info(msg.Info)
	}
	if msg.Shared != "" && m.opts.Agent != nil {
		m.opts.Agent.AddContext(msg.Shared)
	}
	if msg.Compose != "" {
		if strings.TrimSpace(m.input.Value()) == "" {
			m.input.SetValue(msg.Compose)
			m.input.CursorEnd()
		} else {
			m.transcript.info("draft (your input box was not empty, so it is here instead):\n" + msg.Compose)
		}
	}
	if msg.Note != "" {
		m.note(msg.Note)
	}
	m.refresh(false)
}

// gitflowTimeout bounds a drafting call, a push or a gh request.
const gitflowTimeout = 2 * time.Minute

// gitCommand is /commit, /pr, /pr-comments and /issue. A draft lands in
// the input box ("/commit -m …", "/pr create …") for the user to edit and
// send, so nothing is committed or pushed until they do.
func (m *model) gitCommand(name, arg string) {
	var ask gitflow.Asker
	var attach gitflow.AttachFunc
	if ag := m.opts.Agent; ag != nil {
		ask, attach = ag, ag.ProofAttacher(m.opts.Cwd)
	}
	send, life, cwd := m.send, m.life, m.opts.Cwd
	go func() {
		ctx, cancel := context.WithTimeout(life, gitflowTimeout)
		defer cancel()
		send(gitflowMsg{gitflow.Route(ctx, ask, attach, cwd, name, arg)})
	}()
	m.note(gitflow.Status(name, arg))
	m.refresh(false)
}

// securityReviewCommand is /security-review: two read-only subagents
// look at the diff with a security brief, and their findings come back
// as one list, most severe first, shown and shared with the model.
func (m *model) securityReviewCommand(arg string) {
	task := m.taskTool()
	if task == nil {
		m.transcript.errorf("no task tool in this session")
		m.refresh(false)
		return
	}
	diff, err := agent.ReviewDiff(m.life, m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("security review: %v", err)
		m.refresh(false)
		return
	}
	if strings.TrimSpace(diff) == "" && arg == "" {
		m.note("nothing to review (working tree clean)")
		m.refresh(false)
		return
	}
	m.openOverlay(overlayAgents)
	send, life := m.send, m.life
	go func() {
		report := agent.RunSecurityReview(life, task, arg, diff)
		send(gitflowMsg{gitflow.Result{
			Info:   report,
			Shared: agent.SecurityReviewShared(report),
			Note:   "security review done · the findings are shared with the model",
		}})
	}()
	m.note("started security review · two subagents · /agents watches them · the ranked report follows")
	m.refresh(false)
}
