package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/cua"
	"github.com/xmonader/janetcode/internal/mcp"
)

func (m *model) cmdCua(text string, fields []string) (tea.Cmd, bool) {
	arg := ""
	if len(fields) > 1 {
		arg = strings.ToLower(fields[1])
	}
	switch arg {
	case "", "status":
		m.transcript.info(m.cuaStatus())
	case "on":
		m.cuaOn()
	case "off":
		m.cuaOff()
	case "doctor":
		m.transcript.info(m.cuaDoctor())
	default:
		m.transcript.errorf("usage: /cua [on|off|doctor]")
	}
	return nil, false
}

func (m *model) cuaStatus() string {
	var sb strings.Builder
	on := m.opts.Config != nil && m.opts.Config.ComputerUseEnabled()
	fmt.Fprintf(&sb, "computer_use %s", onOff(on))
	d, ok := cua.Detect()
	if !ok {
		sb.WriteString("\nno driver on PATH\n" + cua.InstallHint)
		return sb.String()
	}
	fmt.Fprintf(&sb, "\ndriver %s at %s", d.Kind, d.Path)
	if m.opts.MCP != nil && m.opts.MCP.Manager != nil {
		if _, running := m.opts.MCP.Manager.Client(cua.ServerName); running {
			sb.WriteString("\nMCP server cua-driver is running · tools are behind tool_search as mcp__cua-driver__*")
		} else if on {
			sb.WriteString("\nMCP server is not up yet · /cua on starts it")
		} else {
			sb.WriteString("\n/cua on starts it for this session and later ones")
		}
	}
	return sb.String()
}

func (m *model) cuaDoctor() string {
	d, ok := cua.Detect()
	if !ok {
		return "no driver on PATH\n" + cua.InstallHint
	}
	return d.Doctor(m.life)
}

func (m *model) cuaOn() {
	d, ok := cua.Detect()
	if !ok {
		m.transcript.errorf("no Cua Driver on PATH\n%s", cua.InstallHint)
		return
	}
	if m.opts.Config != nil {
		on := true
		m.opts.Config.ComputerUse = &on
		if m.opts.ConfigPath != "" {
			if err := config.UpdateKey(m.opts.ConfigPath, "computer_use", true); err != nil {
				m.transcript.errorf("save config: %v", err)
				return
			}
		}
	}
	if m.opts.MCP == nil || m.opts.MCP.Manager == nil {
		m.note("computer_use on · restart the session to start " + d.Kind)
		return
	}
	if _, running := m.opts.MCP.Manager.Client(cua.ServerName); running {
		m.note("cua-driver is already running")
		return
	}
	cfg := d.Config()
	m.note("starting " + d.Kind + "…")
	m.opts.MCP.Manager.Start(m.life, cua.ServerName, cfg, func(name string, c *mcp.Client, err error) {
		n := 0
		if c != nil {
			n = len(c.Tools())
		}
		m.send(mcpDoneMsg{name: name, tools: n, err: err})
	})
}

func (m *model) cuaOff() {
	if m.opts.Config != nil {
		off := false
		m.opts.Config.ComputerUse = &off
		if m.opts.ConfigPath != "" {
			_ = config.UpdateKey(m.opts.ConfigPath, "computer_use", false)
		}
	}
	if m.opts.MCP != nil && m.opts.MCP.Manager != nil {
		m.opts.MCP.Manager.Stop(cua.ServerName)
	}
	m.note("computer_use off · cua-driver MCP server stopped")
}
