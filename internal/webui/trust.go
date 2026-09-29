package webui

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/trust"
)

// trustChoices is the line under the report saying how to choose.
const trustChoices = "/trust full · /trust instructions · /trust untrusted · add \"session\" to hold it only until exit. Skills, agents, commands and rules follow at once; hooks, MCP servers, verify commands and allow rules follow at the next start"

// askTrust puts the first-run question in the transcript when the project
// ships capability files and nobody has chosen a level for them.
func (s *server) askTrust() {
	g := s.opts.TrustGate
	if !g.NeedsDecision(s.cwd()) {
		return
	}
	st := g.State(s.cwd())
	s.sheet("Trust this project?", strings.TrimRight(st.Report(), "\n")+"\nNothing from it is used until you choose.\n"+trustChoices)
}

// trustCommand is /trust in the browser: the report, or a level to set.
func (s *server) trustCommand(arg string) {
	g := s.opts.TrustGate
	if g == nil {
		s.errf("project trust is not available in this session")
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		s.sheet("project trust", strings.TrimRight(g.State(s.cwd()).Report(), "\n")+"\n"+trustChoices)
		return
	}
	lvl, err := trust.ParseLevel(fields[0])
	if err != nil {
		s.errf("%v", err)
		return
	}
	persistent := !(len(fields) > 1 && fields[1] == "session")
	if err := g.Set(s.cwd(), lvl, persistent); err != nil {
		s.errf("record trust: %v", err)
		return
	}
	if sess := s.sess(); sess != nil {
		_, _ = sess.Append(session.TrustEvent(g.State(s.cwd()), persistent))
	}
	if lvl != trust.Full && s.opts.Agent != nil {
		// The project's verify commands stop at once; its hooks keep
		// only the user's own.
		if gate, _ := s.opts.Agent.VerifyGate(); gate != nil {
			s.opts.Agent.SetVerify(nil, true)
		}
	}
	scope := "recorded"
	if !persistent {
		scope = "this session only"
	}
	s.notice(fmt.Sprintf("project trust: %s (%s)", lvl, scope))
}
