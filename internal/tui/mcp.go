package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/mcp"
)

// mcpAuthMsg carries progress of /mcp auth; done marks the end.
type mcpAuthMsg struct {
	name string
	text string
	done bool
	err  error
}

// mcpDoneMsg reports a server that came up or failed.
type mcpDoneMsg struct {
	name  string
	tools int
	err   error
}

// mcpPromptMsg carries a rendered MCP prompt to submit.
type mcpPromptMsg struct {
	command string
	text    string
	err     error
}

// startMCP launches the configured servers in the background.
func (m *model) startMCP() {
	o := m.opts.MCP
	if o == nil || o.Manager == nil {
		return
	}
	start := func(servers map[string]mcp.ServerConfig) {
		for name, cfg := range servers {
			o.Manager.Start(m.life, name, cfg, func(name string, c *mcp.Client, err error) {
				n := 0
				if c != nil {
					n = len(c.Tools())
				}
				m.send(mcpDoneMsg{name: name, tools: n, err: err})
			})
		}
	}
	start(o.Global)
	if o.ProjectTrusted {
		start(o.Project)
	} else if len(o.Project) > 0 {
		names := make([]string, 0, len(o.Project))
		for n := range o.Project {
			names = append(names, sanitize(n))
		}
		sort.Strings(names)
		m.transcript.info(fmt.Sprintf("%s%s lists MCP servers (%s) that have not been trusted here · they would run commands on your machine: inspect the file, then /mcp trust starts them", noteNotice, mcp.ProjectFile, strings.Join(names, ", ")))
	}
	if n := len(o.Global) + len(o.Project); n > 0 && (o.ProjectTrusted || len(o.Global) > 0) {
		m.status = "starting MCP servers…"
	}
	m.refresh(false)
}

// mcpPrompts lists the prompts one server offers.
func (m *model) mcpPrompts(server string) []mcp.PromptInfo {
	if m.opts.MCP == nil || m.opts.MCP.Manager == nil {
		return nil
	}
	c, ok := m.opts.MCP.Manager.Client(server)
	if !ok {
		return nil
	}
	return c.Prompts()
}

// runMCPPrompt resolves "/mcp__server__prompt arg1 arg2" against the servers:
// positional words fill the prompt's declared arguments in order, the last
// one taking the rest of the line. The rendered text is submitted as the
// user's turn.
func (m *model) runMCPPrompt(name string, rest string) (tea.Cmd, bool) {
	if m.opts.MCP == nil || m.opts.MCP.Manager == nil || !strings.HasPrefix(name, "/mcp__") {
		return nil, false
	}
	for _, st := range m.opts.MCP.Manager.Status() {
		for _, p := range st.Prompts {
			if "/"+mcp.ToolName(st.Name, p.Name) != name {
				continue
			}
			c, ok := m.opts.MCP.Manager.Client(st.Name)
			if !ok {
				return nil, false
			}
			args := map[string]string{}
			words := strings.Fields(rest)
			for i, a := range p.Arguments {
				switch {
				case i >= len(words):
					if a.Required {
						m.transcript.errorf("%s needs <%s>", name, a.Name)
						m.refresh(false)
						return nil, true
					}
				case i == len(p.Arguments)-1:
					args[a.Name] = strings.Join(words[i:], " ")
				default:
					args[a.Name] = words[i]
				}
			}
			m.startTurn("rendering prompt from " + st.Name)
			send, life, promptName := m.send, m.life, p.Name
			go func() {
				ctx, cancel := context.WithTimeout(life, 60*time.Second)
				defer cancel()
				text, err := c.GetPrompt(ctx, promptName, args)
				send(mcpPromptMsg{command: name, text: text, err: err})
			}()
			return m.spinner.Tick, true
		}
	}
	return nil, false
}

// mcpCommand answers /mcp. It returns a command when what was asked for
// runs in the background (adding a server) or opens a dialog.
func (m *model) mcpCommand(args []string) tea.Cmd {
	o := m.opts.MCP
	if o == nil || o.Manager == nil {
		m.transcript.info("MCP is not configured: add servers under \"mcp_servers\" in the config file or a .mcp.json in the project")
		return nil
	}
	if len(args) > 0 && (args[0] == "auth" || args[0] == "logout") {
		m.mcpAuth(args)
		return nil
	}
	switch {
	case len(args) > 0 && args[0] == "add":
		return m.mcpAdd(args[1:])
	case len(args) > 0 && (args[0] == "remove" || args[0] == "rm"):
		return m.mcpRemove(args[1:])
	}
	if len(args) > 0 && args[0] == "trust" {
		switch {
		case len(o.Project) == 0:
			m.transcript.errorf("no %s in %s", mcp.ProjectFile, shortPath(m.opts.Cwd))
		case o.ProjectTrusted:
			m.note("project servers are already trusted")
		default:
			if err := o.Trust.Trust(m.opts.Cwd, o.ProjectHash); err != nil {
				m.transcript.errorf("record trust: %v", err)
				return nil
			}
			o.ProjectTrusted = true
			m.note("trusted " + mcp.ProjectFile + " · starting its servers")
			for name, cfg := range o.Project {
				o.Manager.Start(m.life, name, cfg, func(name string, c *mcp.Client, err error) {
					n := 0
					if c != nil {
						n = len(c.Tools())
					}
					m.send(mcpDoneMsg{name: name, tools: n, err: err})
				})
			}
		}
		return nil
	}
	st := o.Manager.Status()
	if len(st) == 0 {
		if len(o.Project) > 0 && !o.ProjectTrusted {
			m.transcript.info("no MCP servers running · /mcp trust starts the ones in " + mcp.ProjectFile)
		} else {
			m.transcript.info("no MCP servers configured · /mcp add walks through adding one")
		}
		return nil
	}
	var sb strings.Builder
	sb.WriteString("MCP servers\n")
	for _, s := range st {
		switch {
		case s.Starting:
			fmt.Fprintf(&sb, "  … %s starting\n", s.Name)
		case s.Err != nil:
			fmt.Fprintf(&sb, "  ✗ %s: %v\n", s.Name, s.Err)
		default:
			fmt.Fprintf(&sb, "  ✓ %s · %d tools\n", s.Name, len(s.Tools))
			for _, t := range s.Tools {
				fmt.Fprintf(&sb, "      %s\n", t)
			}
			for _, p := range s.Prompts {
				fmt.Fprintf(&sb, "      /%s  (prompt)\n", mcp.ToolName(s.Name, p.Name))
			}
		}
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	return nil
}

// mcpAuth handles /mcp auth <server> and /mcp logout <server>.
func (m *model) mcpAuth(args []string) {
	o := m.opts.MCP
	if len(args) != 2 {
		m.transcript.errorf("usage: /mcp %s <server>", args[0])
		return
	}
	name := args[1]
	cfg, ok := o.Global[name]
	if !ok {
		cfg, ok = o.Project[name]
	}
	switch {
	case !ok:
		m.transcript.errorf("no MCP server named %q in the config or %s", name, mcp.ProjectFile)
		return
	case cfg.URL == "":
		m.transcript.errorf("mcp %s runs locally (a command); only remote servers use OAuth", name)
		return
	case o.Tokens == nil:
		m.transcript.errorf("OAuth is not available in this session (no token store)")
		return
	}
	if args[0] == "logout" {
		if _, had := o.Tokens.Get(cfg.URL); !had {
			m.note("mcp " + name + ": no token stored")
			return
		}
		if err := o.Tokens.Delete(cfg.URL); err != nil {
			m.transcript.errorf("forget token: %v", err)
			return
		}
		m.note("mcp " + name + ": token forgotten · the server will need /mcp auth again")
		return
	}
	// A failed start may have told us where the server's metadata lives.
	var meta string
	for _, st := range o.Manager.Status() {
		var ae *mcp.AuthError
		if st.Name == name && errors.As(st.Err, &ae) {
			meta = ae.ResourceMetadata
		}
	}
	openURL := o.OpenURL
	if openURL == nil {
		openURL = mcp.OpenBrowser
	}
	m.transcript.info("mcp " + name + ": starting authorization · the browser will open; the URL is also printed here")
	send, life := m.send, m.life
	go func() {
		ctx, cancel := context.WithTimeout(life, 5*time.Minute)
		defer cancel()
		_, err := mcp.Authorize(ctx, mcp.AuthOptions{
			ServerURL: cfg.URL, Store: o.Tokens, ResourceMetadata: meta,
			ClientID: cfg.ClientID, ClientSecret: os.ExpandEnv(cfg.ClientSecret), Scope: cfg.Scope,
			OpenURL: openURL,
			Notify:  func(s string) { send(mcpAuthMsg{name: name, text: s}) },
		})
		send(mcpAuthMsg{name: name, done: true, err: err})
	}()
}

// restartMCP stops a server and starts it again with the current tokens.
func (m *model) restartMCP(name string) {
	o := m.opts.MCP
	cfg, ok := o.Global[name]
	if !ok {
		if cfg, ok = o.Project[name]; !ok || !o.ProjectTrusted {
			return
		}
	}
	o.Manager.Stop(name)
	o.Manager.Start(m.life, name, cfg, func(name string, c *mcp.Client, err error) {
		n := 0
		if c != nil {
			n = len(c.Tools())
		}
		m.send(mcpDoneMsg{name: name, tools: n, err: err})
	})
}
