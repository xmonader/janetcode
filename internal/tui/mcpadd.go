package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcpcmd"
)

// /mcp add: the dialog, the typed form and /mcp remove. What they all end
// up doing lives in internal/mcp/mcpcmd; this file is the terminal's half
// of it — the step dialog, and the confirmations.

// mcpCmdMsg carries the outcome of an /mcp add or /mcp remove that ran in
// the background (writing the file, starting the server).
type mcpCmdMsg struct {
	res cmdresult.Result
	err error
}

// mcpProjectMsg carries the project file a background add or remove
// wrote, so the model's own copy is updated on the UI loop rather than
// from the goroutine that did the writing.
type mcpProjectMsg struct {
	servers map[string]mcp.ServerConfig
	hash    string
}

// mcpFetchMsg carries what a repository declares, for confirmation.
type mcpFetchMsg struct {
	man *mcpcmd.Manifest
	err error
}

// mcpEnv describes the session to the shared add/remove commands. It
// reports false when MCP is not configured at all.
func (m *model) mcpEnv() (mcpcmd.Env, bool) {
	o := m.opts.MCP
	if o == nil {
		return mcpcmd.Env{}, false
	}
	send := m.send
	env := mcpcmd.Env{
		Cwd: m.opts.Cwd, Manager: o.Manager, Trust: o.Trust, Global: o.Global,
		Updated: func(servers map[string]mcp.ServerConfig, hash string) {
			send(mcpProjectMsg{servers: servers, hash: hash})
		},
	}
	if m.opts.ConfigDir != "" {
		env.InstallDir = filepath.Join(m.opts.ConfigDir, "mcp-servers")
	}
	return env, true
}

// mcpRun does the work off the UI loop and reports it as a message, so
// starting a server does not freeze the interface.
func (m *model) mcpRun(status string, fn func(env mcpcmd.Env) (cmdresult.Result, error)) tea.Cmd {
	env, ok := m.mcpEnv()
	if !ok {
		m.transcript.errorf("MCP is not configured in this session")
		m.refresh(false)
		return nil
	}
	m.status = status
	m.refresh(false)
	send := m.send
	go func() {
		res, err := fn(env)
		send(mcpCmdMsg{res: res, err: err})
	}()
	return m.spinner.Tick
}

// mcpAdd handles "/mcp add" in all its forms: no arguments opens the
// dialog, --from installs what a repository declares, and a name with a
// command or URL writes it straight away.
func (m *model) mcpAdd(args []string) tea.Cmd {
	if len(args) == 0 {
		return m.openMCPAdd()
	}
	spec, err := mcp.ParseAdd(args)
	if err != nil {
		m.transcript.errorf("%v", err)
		m.refresh(false)
		return nil
	}
	if spec.From != "" {
		return m.mcpAddFrom(spec)
	}
	return m.mcpRun("adding MCP server "+spec.Name+"…", func(env mcpcmd.Env) (cmdresult.Result, error) {
		return mcpcmd.Add(m.life, env, spec.Name, spec.Config)
	})
}

// mcpAddFrom reads a repository, shows what it declares and asks before
// writing anything.
func (m *model) mcpAddFrom(spec mcp.AddSpec) tea.Cmd {
	env, ok := m.mcpEnv()
	if !ok {
		m.transcript.errorf("MCP is not configured in this session")
		m.refresh(false)
		return nil
	}
	m.status = "reading " + spec.From + "…"
	m.refresh(false)
	send, from, yes := m.send, spec.From, spec.Yes
	go func() {
		man, err := mcpcmd.Fetch(m.life, env, from)
		if err != nil || !yes {
			send(mcpFetchMsg{man: man, err: err})
			return
		}
		res, err := mcpcmd.Install(m.life, env, man)
		send(mcpCmdMsg{res: res, err: err})
	}()
	return m.spinner.Tick
}

// handleMCPFetch shows what a repository would add and asks to confirm.
func (m *model) handleMCPFetch(msg mcpFetchMsg) tea.Cmd {
	m.status = ""
	if msg.err != nil {
		m.transcript.errorf("%v", msg.err)
		m.refresh(false)
		return nil
	}
	m.transcript.info(msg.man.Preview())
	man := msg.man
	return m.confirm("Add "+strings.Join(man.Names, ", ")+"?",
		"they are written to "+mcp.ProjectFile+" and started · esc leaves the project untouched",
		"add them", func() tea.Cmd {
			return m.mcpRun("installing MCP servers…", func(env mcpcmd.Env) (cmdresult.Result, error) {
				return mcpcmd.Install(m.life, env, man)
			})
		})
}

// mcpRemove drops a server from the project file, after confirming.
func (m *model) mcpRemove(args []string) tea.Cmd {
	o := m.opts.MCP
	if o == nil {
		m.transcript.errorf("MCP is not configured in this session")
		m.refresh(false)
		return nil
	}
	yes := false
	var names []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			names = append(names, a)
		}
	}
	if len(names) != 1 {
		m.transcript.errorf("usage: /mcp remove <server>")
		m.refresh(false)
		return nil
	}
	name := names[0]
	if _, ok := o.Project[name]; !ok {
		if _, global := o.Global[name]; global {
			m.transcript.errorf("mcp %s comes from the config file, not %s · remove it there", name, mcp.ProjectFile)
		} else {
			m.transcript.errorf("no MCP server named %q in %s", name, mcp.ProjectFile)
		}
		m.refresh(false)
		return nil
	}
	drop := func() tea.Cmd {
		return m.mcpRun("removing MCP server "+name+"…", func(env mcpcmd.Env) (cmdresult.Result, error) {
			return mcpcmd.Remove(m.life, env, name)
		})
	}
	if yes {
		return drop()
	}
	return m.confirm("Remove "+name+"?",
		mcp.Describe(o.Project[name])+" · it is stopped and dropped from "+mcp.ProjectFile,
		"remove it", drop)
}

// confirm asks a yes/no question in the searchable list every other modal
// uses, rather than inventing a second kind of prompt.
func (m *model) confirm(title, hint, yes string, then func() tea.Cmd) tea.Cmd {
	return m.openPicker(&picker{
		title: title, hint: hint,
		rows: []row{
			{key: yes, cells: []string{yes}, name: "yes"},
			{key: "cancel", cells: []string{"cancel"}, name: "no"},
		},
		choose: func(r row, _ string) tea.Cmd {
			if r.name != "yes" {
				m.note("nothing changed")
				m.refresh(false)
				return nil
			}
			return then()
		},
	})
}

// mcpDraft is what the dialog is building.
type mcpDraft struct {
	name  string
	cfg   mcp.ServerConfig
	pairs map[string]string // env vars or headers, by name
}

// openMCPAdd walks through name → transport → command or URL → optional
// environment or headers → confirm, on the step machine /connect's dialog
// gave the interface.
func (m *model) openMCPAdd() tea.Cmd {
	if _, ok := m.mcpEnv(); !ok {
		m.transcript.errorf("MCP is not configured in this session")
		m.refresh(false)
		return nil
	}
	d := &mcpDraft{pairs: map[string]string{}}
	w := &wizard{title: "Add an MCP server"}
	w.steps = []wizardStep{{
		name:        "name",
		hint:        "The name its tools are prefixed with: mcp__<name>__<tool>.",
		placeholder: "name, e.g. docs",
		enter: func(_ row, typed string) (int, string) {
			if err := mcp.ValidServerName(typed); err != nil {
				return 0, err.Error()
			}
			d.name = typed
			return wizNext, ""
		},
	}, {
		name: "transport",
		hint: "A command runs on this machine; a URL is a server somewhere else.",
		rows: []row{
			{key: "stdio command local", cells: []string{"stdio", "run a command here", "npx, uvx, a binary…"}, name: "stdio"},
			{key: "http remote url streamable", cells: []string{"http", "a remote server", "Streamable HTTP, falling back to SSE"}, name: "http"},
			{key: "sse remote url", cells: []string{"sse", "a remote server, SSE only", "for servers that offer nothing else"}, name: "sse"},
		},
		placeholder: "stdio, http or sse",
		enter: func(r row, _ string) (int, string) {
			d.cfg = mcp.ServerConfig{}
			if r.name != "stdio" {
				d.cfg.Type = r.name
			}
			w.steps[2].hint = mcpTargetHint(r.name)
			w.steps[2].placeholder = mcpTargetPlaceholder(r.name)
			w.steps[3].hint = mcpPairHint(r.name)
			w.steps[3].placeholder = mcpPairPlaceholder(r.name)
			return wizNext, ""
		},
	}, {
		name: "command",
		enter: func(_ row, typed string) (int, string) {
			words := strings.Fields(typed)
			cfg, err := mcp.ParseTarget(words)
			if err != nil {
				return 0, err.Error()
			}
			if want := d.cfg.Type != ""; want != (cfg.URL != "") {
				if want {
					return 0, "that transport needs a URL starting with http:// or https://"
				}
				return 0, "a stdio server needs a command, not a URL"
			}
			cfg.Type = d.cfg.Type
			d.cfg = cfg
			w.steps[4].hint = d.name + " · " + mcp.Describe(d.cfg)
			return wizNext, ""
		},
	}, {
		name:     "settings",
		optional: true,
		enter: func(_ row, typed string) (int, string) {
			if typed == "" {
				d.apply()
				w.steps[4].hint = d.name + " · " + mcp.Describe(d.cfg)
				return wizNext, ""
			}
			k, v, err := mcp.ParsePair(typed)
			if err != nil {
				return 0, err.Error()
			}
			d.pairs[k] = v
			d.apply()
			w.steps[3].hint = mcpPairsSoFar(d)
			return wizStay, ""
		},
	}, {
		name: "confirm",
		rows: []row{
			{key: "add write start", cells: []string{"add it", "write it to " + mcp.ProjectFile + " and start it"}, name: "yes"},
			{key: "cancel", cells: []string{"cancel", "change nothing"}, name: "no"},
		},
		placeholder: "enter adds it",
		enter: func(r row, _ string) (int, string) {
			if r.name != "yes" {
				d.name = "" // done sees this and says nothing was written
			}
			return wizDone, ""
		},
	}}
	w.done = func() tea.Cmd {
		if d.name == "" {
			m.note("nothing added")
			m.refresh(false)
			return nil
		}
		return m.mcpRun("adding MCP server "+d.name+"…", func(env mcpcmd.Env) (cmdresult.Result, error) {
			return mcpcmd.Add(m.life, env, d.name, d.cfg)
		})
	}
	return m.openWizard(w)
}

// apply folds the entered pairs into the draft as environment variables
// or as request headers, whichever the transport takes.
func (d *mcpDraft) apply() {
	if len(d.pairs) == 0 {
		return
	}
	pairs := map[string]string{}
	for k, v := range d.pairs {
		pairs[k] = v
	}
	if d.cfg.URL != "" {
		d.cfg.Headers = pairs
	} else {
		d.cfg.Env = pairs
	}
}

func mcpTargetHint(transport string) string {
	if transport == "stdio" {
		return "The command and its arguments, as you would type them in a shell."
	}
	return "The server's endpoint, as its documentation gives it."
}

func mcpTargetPlaceholder(transport string) string {
	if transport == "stdio" {
		return "npx -y some-mcp-server"
	}
	return "https://host/mcp"
}

func mcpPairHint(transport string) string {
	if transport == "stdio" {
		return "Environment variables for the command, one per line: KEY=value. Enter on an empty line continues."
	}
	return "Request headers, one per line: Name: value. Enter on an empty line continues."
}

func mcpPairPlaceholder(transport string) string {
	if transport == "stdio" {
		return "API_TOKEN=… (optional)"
	}
	return "Authorization: Bearer … (optional)"
}

// mcpPairsSoFar lists what has been entered, so the step shows its own
// accumulation rather than swallowing it.
func mcpPairsSoFar(d *mcpDraft) string {
	names := make([]string, 0, len(d.pairs))
	for k := range d.pairs {
		names = append(names, k)
	}
	sort.Strings(names)
	return fmt.Sprintf("%s · another one, or enter on an empty line to continue", strings.Join(names, ", "))
}
