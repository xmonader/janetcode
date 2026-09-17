package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/sleepfold"
)

// extractMemory runs memory extraction in the background.
func (m *model) extractMemory() tea.Cmd {
	ag, store := m.opts.Agent, m.opts.Memory
	msgs := ag.MessagesSnapshot()
	// Extraction reads a window of prose, not events, so its evidence is
	// the watermark form: this session, as it stands now.
	ref := ""
	if s := m.opts.Session; s != nil {
		if ev := s.Events(); len(ev) > 0 {
			ref = fmt.Sprintf("%s#~%d", s.Meta.ID, ev[len(ev)-1].Seq)
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.life, 2*time.Minute)
		defer cancel()
		xs, err := memory.Extract(ctx, ag.Settings().Provider, ag.Settings().Model, msgs, store.Entries())
		if err != nil {
			return memoryMsg{err: err}
		}
		if ref != "" {
			xs = memory.StampEvidence(xs, ref)
		}
		changed, err := store.Apply(xs)
		return memoryMsg{added: changed, err: err}
	}
}

func (m *model) dreamMemory() tea.Cmd {
	ag, store := m.opts.Agent, m.opts.Memory
	if store == nil || ag == nil {
		m.transcript.errorf("memory is not available in this session")
		return nil
	}
	m.note("consolidating memory…")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.life, 2*time.Minute)
		defer cancel()
		ops, err := memory.Dream(ctx, ag.Settings().Provider, ag.Settings().Model, store.Entries())
		if err != nil {
			return memoryMsg{err: err}
		}
		n, err := store.ApplyDream(ops)
		if err != nil {
			return memoryMsg{err: err}
		}
		if n == 0 {
			return memoryMsg{note: "memory already tight"}
		}
		return memoryMsg{note: fmt.Sprintf("dream: %d memory update(s)", n)}
	}
}

// sleepMemory consolidates what the current session's record established
// — the goal and its outcome, the harness's interventions, what was
// refused, what the user asked — into memory entries that cite the record
// lines they stand on. loud reports an empty result; the automatic
// trigger stays quiet about it.
func (m *model) sleepMemory(loud bool) tea.Cmd {
	ag, store, sess := m.opts.Agent, m.opts.Memory, m.opts.Session
	if store == nil || ag == nil || sess == nil {
		m.transcript.errorf("memory is not available in this session")
		return nil
	}
	in := sleepfold.Fold(sess.Events())
	in.SessionID = sess.Meta.ID
	if loud {
		m.note("reading the session's record…")
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.life, 2*time.Minute)
		defer cancel()
		xs, err := memory.Sleep(ctx, ag.Settings().Provider, ag.Settings().Model, in, store.Entries())
		if err != nil {
			return memoryMsg{err: err}
		}
		changed, err := store.ApplyWith(xs, "sleep")
		if err != nil {
			return memoryMsg{err: err}
		}
		if len(changed) == 0 {
			if loud {
				return memoryMsg{note: "sleep: the record holds nothing new worth keeping"}
			}
			return memoryMsg{}
		}
		return memoryMsg{added: changed}
	}
}

// maybeSleep starts the consolidation once per finished goal: the session
// just answered for a whole piece of work, which is when its record is
// complete enough to be worth reading and cheap enough to be one call.
func (m *model) maybeSleep() tea.Cmd {
	sess, ag := m.opts.Session, m.opts.Agent
	if sess == nil || ag == nil || m.opts.Memory == nil || ag.Off["sleep"] {
		return nil
	}
	gs := agent.FoldGoals(sess.Events())
	if len(gs) == 0 {
		return nil
	}
	g := gs[len(gs)-1]
	if g.Phase != agent.GoalComplete && g.Phase != agent.GoalBlocked {
		return nil
	}
	if m.sleptGoals == nil {
		m.sleptGoals = map[string]bool{}
	}
	if m.sleptGoals[g.ID] {
		return nil
	}
	m.sleptGoals[g.ID] = true
	return m.sleepMemory(false)
}

func memoryLine(e memory.Entry) string {
	scope := ""
	if e.Scope == "global" {
		scope = ", global"
	}
	line := fmt.Sprintf("%s (%s%s): %s", e.Name, e.Type, scope, e.Description)
	if e.Closed() {
		line += fmt.Sprintf(" · superseded by %s since %s", e.SupersededBy, e.ValidUntil.Format("2006-01-02"))
	}
	return line
}

// memoryCommand implements /memory [query|show <name>], /remember [-g] and
// /forget.
func (m *model) memoryCommand(cmd, arg string) {
	store := m.opts.Memory
	if store == nil {
		m.transcript.errorf("memory is not available in this session")
		return
	}
	switch cmd {
	case "/remember":
		scope := "project"
		for _, flag := range []string{"-g ", "--global "} {
			if strings.HasPrefix(arg, flag) {
				scope, arg = "global", strings.TrimSpace(strings.TrimPrefix(arg, flag))
			}
		}
		if arg == "" {
			m.transcript.errorf("usage: /remember [-g] <what to keep> · -g keeps it about you, for every project")
			return
		}
		e, status, err := store.Put(memory.Entry{Body: arg, Scope: scope, Source: "user"})
		switch {
		case err != nil:
			m.transcript.errorf("remember: %v", err)
		case status == memory.Unchanged:
			m.note("already remembered as " + e.Name)
		case status == memory.Updated:
			m.note("memory updated: " + memoryLine(e))
		default:
			m.note("remembered: " + memoryLine(e))
		}
		// The index is frozen for cache's sake, but a fact the user just
		// typed should be in the very next prompt, not wait for a
		// compaction to be visible.
		if err == nil {
			m.opts.Agent.RefreshExtra()
		}
	case "/forget":
		if arg == "" {
			m.transcript.errorf("usage: /forget <name or number from /memory>")
			return
		}
		gone, err := store.Remove(arg)
		if err != nil {
			m.transcript.errorf("forget: %v", err)
			return
		}
		m.note("forgot " + gone.Name + ": " + gone.Description)
		m.opts.Agent.RefreshExtra()
	default:
		entries := store.Entries()
		if len(entries) == 0 {
			m.transcript.info("nothing remembered yet (project " + store.ProjectKey() + ") · /remember <fact> adds one, -g for something about you; facts are also extracted automatically after turns that change something or give feedback")
			return
		}
		var sb strings.Builder
		switch {
		case strings.HasPrefix(arg, "show "):
			e, ok := store.Get(strings.TrimSpace(strings.TrimPrefix(arg, "show ")))
			if !ok {
				m.transcript.errorf("no memory entry named %q", strings.TrimSpace(strings.TrimPrefix(arg, "show ")))
				return
			}
			fmt.Fprintf(&sb, "%s\n%s\nsource %s · used %d× · %s\n", memoryLine(e), e.Body, e.Source, e.Uses, shortPath(e.Path))
		case arg != "":
			hits := store.SearchCtx(m.life, arg, 8)
			if len(hits) == 0 {
				m.transcript.info("nothing remembered matches " + strconv.Quote(arg))
				return
			}
			for _, e := range hits {
				fmt.Fprintf(&sb, "%s\n    %s\n", memoryLine(e), strings.ReplaceAll(e.Body, "\n", "\n    "))
			}
		default:
			fmt.Fprintf(&sb, "memory: %d entries (%s)\n", len(entries), shortPath(store.Root))
			for i, e := range entries {
				fmt.Fprintf(&sb, "%3d. %s\n", i+1, memoryLine(e))
			}
			sb.WriteString("/memory <words> recalls, /memory show <name> prints one, /forget <name|number> removes")
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	}
}
