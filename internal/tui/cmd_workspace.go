package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
)

func (m *model) sandboxCommand(arg string) {
	var sb *shell.Sandbox
	if ag := m.opts.Agent; ag != nil {
		sb = ag.Sandbox
	}
	status, err := shell.SetSandbox(sb, arg)
	if err != nil {
		m.transcript.errorf("%v", err)
		m.refresh(false)
		return
	}
	m.status = status
	m.refresh(false)
}

func (m *model) sshCommand(arg string) {
	m.retargetWorkspace("ssh", arg, tool.DialSSH)
}

func (m *model) dockerCommand(arg string) {
	m.retargetWorkspace("docker", arg, tool.DialDocker)
}

func (m *model) retargetWorkspace(kind, arg string, dial func(string) (tool.Host, error)) {
	status, err := tool.Retarget(m.opts.Workspace, m.opts.Cwd, kind, arg, dial)
	if err != nil {
		m.transcript.errorf("%v", err)
		m.refresh(false)
		return
	}
	m.status = status
	m.refresh(false)
}

func (m *model) addDirCommand(arg string) {
	if arg == "" {
		ws := m.opts.Workspace
		if ws == nil {
			m.note("no extra directories · /add-dir <path> grants one")
			return
		}
		dirs := ws.ExtraDirs()
		if len(dirs) == 0 {
			m.note("no extra directories · /add-dir <path> grants one")
			return
		}
		m.transcript.info("extra dirs:\n" + strings.Join(dirs, "\n"))
		return
	}
	if err := agent.AddDir(m.opts.Workspace, m.opts.Session, arg); err != nil {
		m.transcript.errorf("%v", err)
		return
	}
	m.note("file tools can use " + arg)
}

func (m *model) configCommand(arg string) {
	if arg == "" {
		m.transcript.info("settable keys: " + strings.Join(config.SettableKeys, ", ") + "\nexample: /config thinking=false")
		return
	}
	key, val, ok := strings.Cut(arg, "=")
	if !ok {
		m.transcript.errorf("usage: /config key=value")
		return
	}
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	applied, err := settingsapply.Apply(m.opts.Config, m.opts.Agent, m.opts.ConfigPath, key, val)
	if err != nil {
		m.transcript.errorf("config: %v", err)
		return
	}
	m.note("config " + key + "=" + fmt.Sprint(applied))
}

func (m *model) doctorCommand() tea.Cmd {
	fn := m.opts.Doctor
	ver := m.opts.Version
	send := m.send
	go func() {
		var buf strings.Builder
		var err error
		if fn != nil {
			err = fn(&buf)
		} else {
			buf.WriteString("janetcode " + ver + "\n[ok] session checkup · pass --doctor on the CLI for a live model call\n")
		}
		send(doctorMsg{report: strings.TrimRight(buf.String(), "\n"), err: err})
	}()
	m.note("doctor…")
	m.refresh(false)
	return nil
}

// lspCommand shows language-server status and known problems.
func (m *model) lspCommand() {
	d := m.opts.LSP
	if d == nil || !d.Enabled() {
		m.transcript.info("diagnostics are off: no language server configured (gopls, pyright-langserver, typescript-language-server, rust-analyzer and clangd are used when on PATH; see \"lsp\" in the config)")
		return
	}
	var sb strings.Builder
	for _, l := range d.Status() {
		sb.WriteString(l + "\n")
	}
	known := d.Known()
	if len(known) == 0 {
		sb.WriteString("no known problems")
	} else {
		sb.WriteString(fmt.Sprintf("%d problems:\n", len(known)))
		if len(known) > 30 {
			known = append(known[:30], "…")
		}
		sb.WriteString(strings.Join(known, "\n"))
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

// hooksCommand lists hooks or trusts the project's hook file.
func (m *model) hooksCommand(args []string) {
	o := m.opts.Hooks
	if o == nil {
		m.transcript.info("hooks are not configured: add them under \"hooks\" in the config file or in " + hook.ProjectFile)
		return
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case o.Project.Len() == 0:
			m.transcript.errorf("no %s in %s", hook.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.note("project hooks are already trusted")
		default:
			if err := o.Trust.Trust(trust.HooksKey(m.opts.Cwd), o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return
			}
			o.ProjectTrusted = true
			// A new runner rather than an edit of the live one, which a
			// running turn may be reading.
			hooks := hook.Runner{Cwd: m.opts.Cwd}
			if live := m.opts.Agent.HookRunner(); live != nil {
				hooks = *live
			}
			cfg, notes := hook.MergeScoped(o.Global, o.Project)
			hooks.Config = cfg
			m.opts.Agent.SetHooks(&hooks)
			for _, n := range notes {
				m.note("hooks: " + n)
			}
			m.note("trusted " + hook.ProjectFile + " · its hooks are active")
		}
		return
	}
	var sb strings.Builder
	n := 0
	for _, src := range []struct {
		label string
		cfg   hook.Config
	}{{"config file", o.Global}, {hook.ProjectFile, o.Project}} {
		for _, ev := range hook.Events {
			for _, h := range src.cfg.For(ev) {
				n++
				match := h.Match
				if match == "" {
					match = "*"
				}
				fmt.Fprintf(&sb, "%-12s %-14s %s  (%s)\n", ev, match, h.Command, src.label)
			}
		}
	}
	if n == 0 {
		m.transcript.info("no hooks configured: add them under \"hooks\" in the config file or in " + hook.ProjectFile)
		return
	}
	if o.Project.Len() > 0 && !o.ProjectTrusted {
		sb.WriteString(hook.ProjectFile + " is present but not trusted; run /hooks trust to enable it\n")
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
}

type doctorMsg struct {
	report string
	err    error
}
