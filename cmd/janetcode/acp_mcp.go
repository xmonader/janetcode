package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/cua"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
)

// acpMCPWait is how long session/new waits for its servers before
// answering: long enough for an npx server's first start, short enough
// that a hung one cannot hold the editor's session forever. A server
// still starting after it keeps starting, and its tools appear when it
// is up.
var acpMCPWait = 60 * time.Second

// acpMCP starts MCP servers for the agents an ACP connection hands out:
// the configured ones (and the project's, when trusted) the first time an
// agent is seen, plus whatever servers the editor passes in session/new
// or session/load. The editor is the person's own program, so its
// servers need no project trust; the managed policy still applies.
type acpMCP struct {
	r        *root
	ctx      context.Context
	mu       sync.Mutex
	managers map[*agent.Agent]*mcptool.Manager
}

func (a *acpMCP) start(ag *agent.Agent, root string, client map[string]mcp.ServerConfig) {
	a.mu.Lock()
	m, seen := a.managers[ag]
	if !seen {
		if ag == a.r.ag {
			m = &a.r.servers
		} else {
			m = &mcptool.Manager{Options: a.r.servers.Options, Registry: ag.Tools, Allowed: a.r.servers.Allowed}
			m.Options.Root = root
			a.r.onClose(m.Close)
		}
		a.managers[ag] = m
		m.SetNotify(func(msg string) { fmt.Fprintf(os.Stderr, "[mcp] %s\n", msg) })
	}
	a.mu.Unlock()
	toStart := map[string]mcp.ServerConfig{}
	if !seen {
		for n, c := range a.r.cfg.MCPServers {
			toStart[n] = c
		}
		// The project file was judged for the directory janetcode was
		// started in; another directory's is not read.
		if ag == a.r.ag && a.r.projectTrusted {
			for n, c := range a.r.projectServers {
				toStart[n] = c
			}
		}
		if a.r.cfg.ComputerUseEnabled() {
			cua.MergeInto(toStart)
		}
	}
	for n, c := range client {
		if _, running := m.Client(n); running {
			continue
		}
		toStart[n] = c
	}
	if len(toStart) == 0 {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.StartAll(a.ctx, toStart, func(name string, c *mcp.Client, err error) {
			if err != nil {
				fmt.Fprintf(os.Stderr, "[mcp] %s failed: %v\n", name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "[mcp] %s ready: %d tools\n", name, len(c.Tools()))
		})
	}()
	select {
	case <-done:
	case <-time.After(acpMCPWait):
		fmt.Fprintf(os.Stderr, "[mcp] still starting after %s; the session goes on without waiting\n", acpMCPWait)
	case <-a.ctx.Done():
	}
}

// elicitAsker is who answers an MCP server's elicitation/create: the ACP
// server when running as one (the editor shows the prompt), else the
// session's own Asker (the TUI or web question UI). Nil declines.
func (r *root) elicitAsker(ag *agent.Agent) agent.Asker {
	if s := r.acpAsker; s != nil {
		return s
	}
	if ag.Asker == nil {
		return nil
	}
	return ag.Asker
}
