package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
)

// extractMemory runs memory extraction in the background.
func (m *model) extractMemory() tea.Cmd {
	ag, store := m.opts.Agent, m.opts.Memory
	msgs := append([]llm.Message(nil), ag.Messages...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.life, 2*time.Minute)
		defer cancel()
		xs, err := memory.Extract(ctx, ag.Settings().Provider, ag.Settings().Model, msgs, store.Entries())
		if err != nil {
			return memoryMsg{err: err}
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

func memoryLine(e memory.Entry) string {
	scope := ""
	if e.Scope == "global" {
		scope = ", global"
	}
	return fmt.Sprintf("%s (%s%s): %s", e.Name, e.Type, scope, e.Description)
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
