package webui

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/cua"
	"github.com/xmonader/janetcode/internal/mcp"
)

func (s *server) cmdCua(arg string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "", "status":
		s.sheet("cua", s.cuaStatus())
	case "on":
		s.cuaOn()
	case "off":
		s.cuaOff()
	case "doctor":
		s.sheet("cua doctor", s.cuaDoctor())
	default:
		s.errf("usage: /cua [on|off|doctor]")
	}
	return false, nil
}

func (s *server) cuaStatus() string {
	s.cfgMu.RLock()
	on := s.opts.Config != nil && s.opts.Config.ComputerUseEnabled()
	s.cfgMu.RUnlock()
	line := "computer_use " + onOff(on)
	d, ok := cua.Detect()
	if !ok {
		return line + "\nno driver on PATH\n" + cua.InstallHint
	}
	line += fmt.Sprintf("\ndriver %s at %s", d.Kind, d.Path)
	if s.opts.MCP != nil && s.opts.MCP.Manager != nil {
		if _, running := s.opts.MCP.Manager.Client(cua.ServerName); running {
			line += "\nMCP server cua-driver is running"
		}
	}
	return line
}

func (s *server) cuaDoctor() string {
	d, ok := cua.Detect()
	if !ok {
		return "no driver on PATH\n" + cua.InstallHint
	}
	return d.Doctor(s.life)
}

func (s *server) cuaOn() {
	d, ok := cua.Detect()
	if !ok {
		s.errf("no Cua Driver on PATH\n%s", cua.InstallHint)
		return
	}
	if s.opts.Config != nil {
		on := true
		s.cfgMu.Lock()
		s.opts.Config.ComputerUse = &on
		s.cfgMu.Unlock()
		if s.opts.ConfigPath != "" {
			if err := config.UpdateKey(s.opts.ConfigPath, "computer_use", true); err != nil {
				s.errf("save config: %v", err)
				return
			}
		}
	}
	if s.opts.MCP == nil || s.opts.MCP.Manager == nil {
		s.notice("computer_use on · restart to start " + d.Kind)
		return
	}
	if _, running := s.opts.MCP.Manager.Client(cua.ServerName); running {
		s.notice("cua-driver is already running")
		return
	}
	s.opts.MCP.Manager.Start(s.life, cua.ServerName, d.Config(), func(name string, c *mcp.Client, err error) {
		if err != nil {
			s.errf("%s: %v", name, err)
			return
		}
		n := 0
		if c != nil {
			n = len(c.Tools())
		}
		s.notice(fmt.Sprintf("%s ready: %d tools", name, n))
	})
}

func (s *server) cuaOff() {
	if s.opts.Config != nil {
		off := false
		s.cfgMu.Lock()
		s.opts.Config.ComputerUse = &off
		s.cfgMu.Unlock()
		if s.opts.ConfigPath != "" {
			_ = config.UpdateKey(s.opts.ConfigPath, "computer_use", false)
		}
	}
	if s.opts.MCP != nil && s.opts.MCP.Manager != nil {
		s.opts.MCP.Manager.Stop(cua.ServerName)
	}
	s.notice("computer_use off")
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
