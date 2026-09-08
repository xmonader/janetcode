package mcp

import (
	"context"
	"sort"
	"sync"

	"github.com/xmonader/janetcode/internal/tool"
)

// Manager owns the running servers and the tools they contributed.
type Manager struct {
	Options  Options
	Registry *tool.Registry // receives each server's tools once it is up

	mu       sync.Mutex
	clients  map[string]*Client
	errs     map[string]error
	starting map[string]bool
}

// ServerStatus is one row of /mcp.
type ServerStatus struct {
	Name     string
	Tools    []string
	Err      error
	Starting bool
}

// Start launches one server in the background. onDone runs when it is up
// or failed; either way the manager remembers the outcome.
func (m *Manager) Start(ctx context.Context, name string, cfg ServerConfig, onDone func(name string, c *Client, err error)) {
	m.mu.Lock()
	if m.clients == nil {
		m.clients, m.errs, m.starting = map[string]*Client{}, map[string]error{}, map[string]bool{}
	}
	if m.starting[name] || m.clients[name] != nil {
		m.mu.Unlock()
		return
	}
	m.starting[name] = true
	delete(m.errs, name)
	m.mu.Unlock()
	go func() {
		c, err := Start(ctx, name, cfg, m.Options)
		m.mu.Lock()
		delete(m.starting, name)
		if err != nil {
			m.errs[name] = err
		} else {
			m.clients[name] = c
			if m.Registry != nil {
				for _, t := range c.AgentTools() {
					m.Registry.Add(t)
				}
			}
		}
		m.mu.Unlock()
		if onDone != nil {
			onDone(name, c, err)
		}
	}()
}

// StartAll launches every server and waits for all of them.
func (m *Manager) StartAll(ctx context.Context, servers map[string]ServerConfig, onDone func(name string, c *Client, err error)) {
	var wg sync.WaitGroup
	for name, cfg := range servers {
		wg.Add(1)
		m.Start(ctx, name, cfg, func(name string, c *Client, err error) {
			defer wg.Done()
			if onDone != nil {
				onDone(name, c, err)
			}
		})
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
		for _, t := range c.AgentTools() {
			st.Tools = append(st.Tools, t.Name())
		}
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
	m.clients = map[string]*Client{}
	m.mu.Unlock()
	for _, c := range clients {
		if m.Registry != nil {
			for _, t := range c.AgentTools() {
				m.Registry.Remove(t.Name())
			}
		}
		c.Close()
	}
}
