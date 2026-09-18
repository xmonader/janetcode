package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/trajectory"
)

// contextCommand shows what fills the context window.
func (m *model) contextCommand() {
	r := m.opts.Agent.Context()
	var sb strings.Builder
	pct := 0
	if r.CompactAt > 0 {
		pct = r.Total * 100 / r.CompactAt
	}
	fmt.Fprintf(&sb, "context ~%s tokens · compaction at %s (%d%%) · %d messages, %d turns\n", textfmt.Tokens(r.Total), textfmt.Tokens(r.CompactAt), pct, r.Messages, r.Turns)
	sb.WriteString(r.PrefixLines())
	fmt.Fprintf(&sb, "  tools         %6s   (%d definitions", textfmt.Tokens(r.Tools), r.ToolCount)
	if r.Deferred > 0 {
		fmt.Fprintf(&sb, ", %d deferred", r.Deferred)
	}
	sb.WriteString(")\n")
	fmt.Fprintf(&sb, "  user text     %6s   assistant text %6s   thinking %6s\n", textfmt.Tokens(r.UserText), textfmt.Tokens(r.AssistantText), textfmt.Tokens(r.Thinking))
	fmt.Fprintf(&sb, "  tool calls    %6s   tool results   %6s", textfmt.Tokens(r.ToolCalls), textfmt.Tokens(r.ToolResults))
	if r.Images > 0 {
		fmt.Fprintf(&sb, "   images %d (~%s)", r.Images, textfmt.Tokens(r.Images*1600))
	}
	sb.WriteString("\n")
	if len(r.ByTool) > 0 {
		sb.WriteString("by tool:")
		for i, t := range r.ByTool {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&sb, " %s %s (%d)", t.Name, textfmt.Tokens(t.Tokens), t.Calls)
		}
		sb.WriteString("\n")
	}
	if len(r.Largest) > 0 {
		sb.WriteString("largest tool results:\n")
		for _, it := range r.Largest {
			subj := it.Subject
			if strings.HasPrefix(subj, "/") {
				subj = m.relPath(subj)
			}
			if subj != "" {
				subj = " " + textfmt.Clip(subj, 50)
			}
			fmt.Fprintf(&sb, "  %6s  %s%s (message %d)\n", textfmt.Tokens(it.Tokens), it.Tool, subj, it.Index)
		}
	}
	if false {
		// The surface meter is a diagnostic; /inspect has it.
		meter := session.Measure(m.sessionEvents(), nil)
		fmt.Fprintf(&sb, "surface meter · log rev %d · %s across %d nodes\n", meter.LogRevision, textfmt.Tokens(meter.Surface), len(meter.Nodes))
		sort.Slice(meter.Nodes, func(i, j int) bool { return meter.Nodes[i].Tokens > meter.Nodes[j].Tokens })
		for i, n := range meter.Nodes {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&sb, "  seq %d  %s  %s\n", n.Seq, n.Kind, textfmt.Tokens(n.Tokens))
		}
	}
	sb.WriteString("/compact summarises the history, /pin keeps a note through it")
	m.transcript.info(sb.String())
}

// sessionEvents is a snapshot of the session log, or nil without a session.
func (m *model) sessionEvents() []session.Event {
	if m.opts.Session == nil {
		return nil
	}
	return m.opts.Session.Events()
}

// inspectCommand reports one part of the session state. Everything the
// harness knows about itself is reachable from here, so a surprising turn
// can be looked into rather than guessed at.
func (m *model) inspectCommand(arg string) {
	if m.opts.Inspect == nil {
		m.transcript.errorf("nothing is registered for inspection in this session")
		return
	}
	ctx, cancel := context.WithTimeout(m.life, 60*time.Second)
	defer cancel()
	out, err := m.opts.Inspect.Report(ctx, arg)
	if err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.transcript.info(out)
}

// trajectoryCommand shows the session as a ledger of records rather
// than a dump of events. The raw log is still one word away (`raw`),
// because when the ledger itself is what looks wrong, the thing to read
// is what is on disk.
func (m *model) trajectoryCommand(args []string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	out, ok := trajectory.Command(m.sessionEvents(), args, max(40, m.width-4-m.gutter()))
	if !ok {
		m.transcript.errorf("%s", out)
		return
	}
	m.transcript.info(out)
}

// whyCommand explains the last thing the harness did on its own. It
// reads the ledger the session already has open and answers from a table
// of explanations: no model call, so asking costs nothing and can be
// asked as often as it is useful.
func (m *model) whyCommand(args []string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	n := 1
	if len(args) > 0 {
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 1 {
			m.transcript.errorf("usage: /why [n] · n counts back from the most recent")
			return
		}
		n = v
	}
	m.transcript.info(agent.WhyLast(m.sessionEvents(), n))
}

func (m *model) feedbackCommand(args []string) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session")
		return
	}
	if len(args) == 0 {
		m.transcript.errorf("usage: /feedback up|down [note]")
		return
	}
	r := session.RateUp
	if args[0] == "down" || args[0] == "-" {
		r = session.RateDown
	} else if args[0] != "up" && args[0] != "+" {
		m.transcript.errorf("usage: /feedback up|down [note]")
		return
	}
	note := strings.Join(args[1:], " ")
	if err := m.opts.Session.Rate(0, r, note); err != nil {
		m.transcript.errorf("feedback: %v", err)
		return
	}
	m.note("recorded " + string(r) + " on last assistant message")
}
