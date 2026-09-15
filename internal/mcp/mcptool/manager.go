package mcptool

import (
	"context"
	"sort"
	"sync"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/tool"
)

// Manager owns the running servers and the tools they contributed.
type Manager struct {
	Options  mcp.Options
	Registry *tool.Registry // receives each server's tools once it is up
	// DeferAbove is the number of tools past which a server's tools are
	// registered deferred (loaded on demand via tool_search) instead of
	// being sent with every request. Zero means DefaultDeferAbove.
	DeferAbove int

	mu       sync.Mutex
	clients  map[string]*mcp.Client
	errs     map[string]error
	starting map[string]bool
}

// DefaultDeferAbove is the per-server tool count above which MCP tools are
// deferred; a handful of tools are cheaper to list than to search for.
const DefaultDeferAbove = 6

// ServerStatus is one row of /mcp.
type ServerStatus struct {
	Name     string
	Tools    []string
	Prompts  []mcp.PromptInfo
	Err      error
	Starting bool
}

// Client returns a running server by name.
func (m *Manager) Client(name string) (*mcp.Client, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[name]
	return c, ok
}

// Start launches one server in the background. onDone runs when it is up
// or failed; either way the manager remembers the outcome.
//
// It reports whether a start was actually begun. A server already running
// or already starting is left alone and its onDone is never called, which
// a caller that waits on onDone has to know about.
func (m *Manager) Start(ctx context.Context, name string, cfg mcp.ServerConfig, onDone func(name string, c *mcp.Client, err error)) bool {
	m.mu.Lock()
	if m.clients == nil {
		m.clients, m.errs, m.starting = map[string]*mcp.Client{}, map[string]error{}, map[string]bool{}
	}
	if m.starting[name] || m.clients[name] != nil {
		m.mu.Unlock()
		return false
	}
	m.starting[name] = true
	delete(m.errs, name)
	m.mu.Unlock()
	go func() {
		c, err := mcp.Start(ctx, name, cfg, m.Options)
		m.mu.Lock()
		delete(m.starting, name)
		if err != nil {
			m.errs[name] = err
		} else {
			m.clients[name] = c
			if m.Registry != nil {
				tools := AgentTools(c)
				deferAbove := m.DeferAbove
				if deferAbove == 0 {
					deferAbove = DefaultDeferAbove
				}
				for _, t := range tools {
					if len(tools) > deferAbove {
						m.Registry.AddDeferred(t)
					} else {
						m.Registry.Add(t)
					}
				}
			}
		}
		m.mu.Unlock()
		if onDone != nil {
			onDone(name, c, err)
		}
	}()
	return true
}

// Stop closes one server and removes its tools, so Start can bring it
// back (after /mcp auth, for instance).
func (m *Manager) Stop(name string) {
	m.mu.Lock()
	c := m.clients[name]
	delete(m.clients, name)
	delete(m.errs, name)
	m.mu.Unlock()
	if c == nil {
		return
	}
	if m.Registry != nil {
		for _, t := range AgentTools(c) {
			m.Registry.Remove(t.Name())
		}
	}
	c.Close()
}

// StartAll launches every server and waits for all of them.
func (m *Manager) StartAll(ctx context.Context, servers map[string]mcp.ServerConfig, onDone func(name string, c *mcp.Client, err error)) {
	var wg sync.WaitGroup
	for name, cfg := range servers {
		wg.Add(1)
		started := m.Start(ctx, name, cfg, func(name string, c *mcp.Client, err error) {
			defer wg.Done()
			if onDone != nil {
				onDone(name, c, err)
			}
		})
		// A server already up or already starting never calls onDone, so
		// waiting for it here would wait forever.
		if !started {
			wg.Done()
		}
	}
	wg.Wait()
}

// Status lists servers, sorted by name.
func (m *Manager) Status() []ServerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []ServerStatus
	for name, c := range m.clients {
		seen[name] = true
		st := ServerStatus{Name: name}
		for _, t := range AgentTools(c) {
			st.Tools = append(st.Tools, t.Name())
		}
		st.Prompts = c.Prompts()
		out = append(out, st)
	}
	for name, err := range m.errs {
		if !seen[name] {
			seen[name] = true
			out = append(out, ServerStatus{Name: name, Err: err})
		}
	}
	for name := range m.starting {
		if !seen[name] {
			out = append(out, ServerStatus{Name: name, Starting: true})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close stops every server and removes its tools.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*mcp.Client{}
	m.mu.Unlock()
	for _, c := range clients {
		if m.Registry != nil {
			for _, t := range AgentTools(c) {
				m.Registry.Remove(t.Name())
			}
		}
		c.Close()
	}
}
