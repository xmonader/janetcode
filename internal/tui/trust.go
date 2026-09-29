package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/trust"
)

// loadCustom reads the custom commands: the user's own always, the
// project's only when its trust level lets instruction files in.
func (m *model) loadCustom() {
	m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(m.opts.TrustGate.ProjectDir(m.opts.Cwd), m.opts.ConfigDir) {
		m.custom["/"+c.Name] = c
	}
}

// trustPicker is the first-run question for a project that ships skills,
// hooks and the rest and has no level chosen for them yet.
func (m *model) trustPicker() tea.Cmd {
	g := m.opts.TrustGate
	st := g.State(m.opts.Cwd)
	kinds := map[string]bool{}
	var names []string
	for _, c := range st.Caps {
		if !kinds[c.Kind] {
			kinds[c.Kind] = true
			names = append(names, c.Kind)
		}
	}
	hint := "this project ships " + strings.Join(names, ", ") + ". Nothing from it is used until you choose; trust never approves a tool call"
	if st.Source == "changed" {
		hint = "this project's " + strings.Join(names, ", ") + " changed since you chose a level. Nothing from it is used until you choose again"
	}
	return m.openPicker(&picker{
		title: "Trust " + shortPath(m.opts.Cwd) + "?",
		hint:  hint,
		rows: []row{
			{key: "full", cells: []string{"full", "skills, agents, commands, rules, and hooks, MCP, verify, allow rules, recipes"}, name: "full"},
			{key: "instructions", cells: []string{"instructions", "skills, agents, commands and rules only; nothing the project ships runs"}, name: "instructions"},
			{key: "session", cells: []string{"full, this session only", "nothing is written down"}, name: "session"},
			{key: "untrusted", cells: []string{"untrusted", "use none of it, and remember that"}, name: "untrusted"},
		},
		choose: func(r row, _ string) tea.Cmd {
			switch r.name {
			case "session":
				return m.trustCommand([]string{"full", "session"})
			case "":
				return nil
			}
			return m.trustCommand([]string{r.name})
		},
		cancel: func() {
			m.note("project not trusted · /trust chooses a level later")
		},
	})
}

// trustCommand is /trust: with no argument the report, with a level it
// records the level (or, followed by "session", holds it until exit) and
// applies what can change at once.
func (m *model) trustCommand(args []string) tea.Cmd {
	g := m.opts.TrustGate
	if g == nil {
		m.transcript.errorf("project trust is not available in this session")
		return nil
	}
	if len(args) == 0 {
		m.transcript.info(strings.TrimRight(g.State(m.opts.Cwd).Report(), "\n") + "\n/trust full · /trust instructions · /trust untrusted · add \"session\" to hold it only until exit")
		return nil
	}
	lvl, err := trust.ParseLevel(args[0])
	if err != nil {
		m.transcript.errorf("%v", err)
		return nil
	}
	persistent := !(len(args) > 1 && args[1] == "session")
	if err := g.Set(m.opts.Cwd, lvl, persistent); err != nil {
		m.transcript.errorf("record trust: %v", err)
		return nil
	}
	m.recordTrust(lvl, persistent)
	m.loadCustom()
	var cmds []tea.Cmd
	switch lvl {
	case trust.Full:
		// Everything the per-file commands would enable, enabled now.
		if o := m.opts.Hooks; o != nil && o.Project.Len() > 0 && !o.ProjectTrusted {
			m.hooksCommand([]string{"trust"})
		}
		if o := m.opts.Verify; o != nil && len(o.Config.Commands) > 0 && !o.ProjectTrusted {
			cmds = append(cmds, m.verifyCommand([]string{"trust"}))
		}
		if o := m.opts.Perms; o != nil && len(o.Project.Allow) > 0 && !o.ProjectTrusted {
			m.permissionsCommand([]string{"trust"})
		}
		if o := m.opts.MCP; o != nil && len(o.Project) > 0 && !o.ProjectTrusted {
			cmds = append(cmds, m.mcpCommand([]string{"trust"}))
		}
	default:
		// What the project already had running stops where it can.
		if o := m.opts.Hooks; o != nil && o.ProjectTrusted {
			hooks := hook.Runner{Cwd: m.opts.Cwd}
			if live := m.opts.Agent.HookRunner(); live != nil {
				hooks = *live
			}
			hooks.Config = o.Global
			m.opts.Agent.SetHooks(&hooks)
			o.ProjectTrusted = false
		}
		if o := m.opts.Verify; o != nil && o.ProjectTrusted {
			m.opts.Agent.SetVerify(nil, true)
			o.ProjectTrusted = false
		}
		var later []string
		if o := m.opts.MCP; o != nil && o.ProjectTrusted {
			for name := range o.Project {
				o.Manager.Stop(name)
			}
			o.ProjectTrusted = false
		}
		if o := m.opts.Perms; o != nil && o.ProjectTrusted {
			o.ProjectTrusted = false
			later = append(later, "allow rules")
		}
		if len(later) > 0 {
			m.note("the project's " + strings.Join(later, ", ") + " stay in force until the next start")
		}
	}
	scope := "recorded"
	if !persistent {
		scope = "this session only"
	}
	m.note(fmt.Sprintf("project trust: %s (%s)", lvl, scope))
	m.refresh(false)
	return tea.Batch(cmds...)
}

// execTrustBlocked reports, and says so, when the project's level rules
// out enabling one of its executable files by itself.
func (m *model) execTrustBlocked() bool {
	g := m.opts.TrustGate
	if g == nil {
		return false
	}
	st := g.State(m.opts.Cwd)
	if st.Decided && st.Level != trust.Full {
		m.transcript.errorf("project trust is %s: /trust full enables the files this project runs", st.Level)
		return true
	}
	return false
}

// recordTrust writes the change to the session log, with the project and
// what it affects, so the log says why a hook started or stopped running.
func (m *model) recordTrust(lvl trust.Level, persistent bool) {
	if m.opts.Session != nil {
		_, _ = m.opts.Session.Append(session.TrustEvent(m.opts.TrustGate.State(m.opts.Cwd), persistent))
	}
}
