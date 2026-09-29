package mcptool

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

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

	// mu guards every map below and closed. Nothing reads them without
	// it, and no client is started or closed while it is held.
	mu       sync.Mutex
	clients  map[string]*mcp.Client
	errs     map[string]error
	starting map[string]bool
	closed   bool

	// Notify, when set, is told when a server that was up goes down and
	// when it comes back: one line for the person, not the model.
	Notify func(msg string)
	// Backoff returns how long to wait before reconnect attempt n (1-based);
	// nil means DefaultBackoff. MaxAttempts bounds the automatic retries
	// (zero means DefaultMaxAttempts); after that the server stays down
	// until /mcp reconnect.
	Backoff     func(attempt int) time.Duration
	MaxAttempts int

	// launch remembers how each server was started, so a server that
	// went away can be started again the same way.
	launch map[string]launchSpec
	// down holds a server that was up and lost its connection: its
	// tools stay registered (a call gets a clear "down" answer instead of
	// "no such tool") until the reconnect replaces them.
	down map[string]*downState
	// gen counts Stops per name; a reconnect begun before a Stop does
	// not bring the server back after it.
	gen map[string]int
}

type launchSpec struct {
	ctx    context.Context
	cfg    mcp.ServerConfig
	onDone func(name string, c *mcp.Client, err error)
}

type downState struct {
	client   *mcp.Client // the lost connection; its tools are still registered
	attempts int
	next     time.Time
	gaveUp   bool
	timer    *time.Timer
}

// DefaultMaxAttempts is how many times a lost server is restarted on its
// own before it is left for /mcp reconnect.
const DefaultMaxAttempts = 8

// DefaultBackoff doubles from one second to a minute.
func DefaultBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < time.Minute; i++ {
		d *= 2
	}
	if d > time.Minute {
		d = time.Minute
	}
	return d
}

// ErrClosed is why a start that was still in flight when the session ended
// reports no server: the manager it would have been registered with is
// gone, so the server it started has been stopped again.
var ErrClosed = errors.New("mcp: the manager was closed while the server was starting")

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
	// Down is a server that was up and lost its connection. Attempts is
	// how many reconnects have been tried, NextRetry when the next one
	// runs, and GaveUp that none will run until /mcp reconnect.
	Down      bool
	Attempts  int
	NextRetry time.Time
	GaveUp    bool
}

// SetNotify replaces Notify while servers may be running.
func (m *Manager) SetNotify(f func(msg string)) {
	m.mu.Lock()
	m.Notify = f
	m.mu.Unlock()
}

// DownLine describes a server that lost its connection, for /mcp: why,
// and when it is tried again or that it will not be until asked.
func (s ServerStatus) DownLine(now time.Time) string {
	line := fmt.Sprintf("%s down", s.Name)
	if s.Err != nil {
		line += fmt.Sprintf(" (%v)", s.Err)
	}
	switch {
	case s.Starting:
		line += fmt.Sprintf(" · reconnecting (attempt %d)", s.Attempts)
	case s.GaveUp:
		line += fmt.Sprintf(" · gave up after %d attempts · /mcp reconnect %s", s.Attempts, s.Name)
	case !s.NextRetry.IsZero():
		wait := s.NextRetry.Sub(now).Round(time.Second)
		if wait < 0 {
			wait = 0
		}
		line += fmt.Sprintf(" · reconnect attempt %d in %s · /mcp reconnect %s tries now", s.Attempts, wait, s.Name)
	}
	return line
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
	m.init()
	if m.closed || m.starting[name] || m.clients[name] != nil {
		m.mu.Unlock()
		return false
	}
	m.launch[name] = launchSpec{ctx: ctx, cfg: cfg, onDone: onDone}
	m.mu.Unlock()
	return m.begin(name, -1, onDone)
}

func (m *Manager) init() {
	if m.clients == nil {
		m.clients = map[string]*mcp.Client{}
	}
	if m.errs == nil {
		m.errs = map[string]error{}
	}
	if m.starting == nil {
		m.starting = map[string]bool{}
	}
	if m.launch == nil {
		m.launch = map[string]launchSpec{}
	}
	if m.down == nil {
		m.down = map[string]*downState{}
	}
	if m.gen == nil {
		m.gen = map[string]int{}
	}
}

// begin starts the server recorded in launch. gen is the Stop generation
// a reconnect was scheduled under, or -1 for a start the user asked for.
func (m *Manager) begin(name string, gen int, onDone func(name string, c *mcp.Client, err error)) bool {
	m.mu.Lock()
	spec, ok := m.launch[name]
	if m.closed || !ok || m.starting[name] || m.clients[name] != nil || (gen >= 0 && m.gen[name] != gen) {
		m.mu.Unlock()
		return false
	}
	m.starting[name] = true
	delete(m.errs, name)
	gen = m.gen[name]
	m.mu.Unlock()
	opts := m.Options
	opts.OnToolsChanged = m.toolsChanged
	ctx := spec.ctx
	if ctx == nil || ctx.Err() != nil {
		// The context the server was first started under may have been a
		// command's, long finished; the manager's own lifetime is Close.
		ctx = context.Background()
	}
	go func() {
		c, err := mcp.Start(ctx, name, spec.cfg, opts)
		m.mu.Lock()
		delete(m.starting, name)
		switch {
		case err == nil && m.gen[name] != gen && !m.closed:
			// Stopped while it was starting: nobody wants it any more.
			m.mu.Unlock()
			c.Close()
			return
		case err != nil:
			m.errs[name] = err
			if st := m.down[name]; st != nil {
				m.scheduleLocked(name, st)
			}
		case m.closed:
			// The session ended while this server was shaking hands. It is
			// not going into the maps — Close has already walked them — so
			// nothing would ever stop it, and an MCP server is usually a
			// wrapper around something that holds a port or an index for
			// the rest of the machine's uptime.
			m.mu.Unlock()
			c.Close()
			if onDone != nil {
				onDone(name, nil, fmt.Errorf("%w: %s", ErrClosed, name))
			}
			return
		default:
			back := m.down[name] != nil
			if st := m.down[name]; st != nil {
				// The lost connection's tools are replaced by the new ones;
				// a tool the server no longer has goes with them.
				m.unregister(st.client)
				delete(m.down, name)
			}
			m.clients[name] = c
			m.register(c)
			go m.watch(name, c)
			if back && m.Notify != nil {
				notify, n := m.Notify, len(c.Tools())
				defer func() { notify(fmt.Sprintf("mcp %s reconnected · %d tools", name, n)) }()
			}
		}
		m.mu.Unlock()
		if onDone != nil {
			onDone(name, c, err)
		}
	}()
	return true
}

// watch waits for a running server's connection to end. A server the
// session stopped on purpose is gone from clients by then; one that went
// away on its own is marked down and restarted with backoff.
func (m *Manager) watch(name string, c *mcp.Client) {
	<-c.Done()
	m.mu.Lock()
	if m.closed || m.clients[name] != c {
		m.mu.Unlock()
		return
	}
	delete(m.clients, name)
	reason := c.Err()
	m.errs[name] = reason
	st := &downState{client: c}
	m.down[name] = st
	m.scheduleLocked(name, st)
	notify := m.Notify
	m.mu.Unlock()
	if notify != nil {
		notify(fmt.Sprintf("mcp %s disconnected (%v) · reconnecting · /mcp reconnect %s retries now", name, reason, name))
	}
}

// scheduleLocked arranges the next reconnect of a down server, or gives
// up once the attempts are spent. Called with mu held.
func (m *Manager) scheduleLocked(name string, st *downState) {
	max := m.MaxAttempts
	if max == 0 {
		max = DefaultMaxAttempts
	}
	if st.attempts >= max {
		st.gaveUp = true
		st.next = time.Time{}
		if notify := m.Notify; notify != nil {
			go notify(fmt.Sprintf("mcp %s is still down after %d attempts · /mcp reconnect %s tries again", name, st.attempts, name))
		}
		return
	}
	st.attempts++
	backoff := m.Backoff
	if backoff == nil {
		backoff = DefaultBackoff
	}
	d := backoff(st.attempts)
	st.next = time.Now().Add(d)
	gen := m.gen[name]
	st.timer = time.AfterFunc(d, func() {
		m.mu.Lock()
		spec := m.launch[name]
		m.mu.Unlock()
		m.begin(name, gen, spec.onDone)
	})
}

// Reconnect restarts a configured server now, whether it is up, down or
// failed, and resets its backoff. It reports false for a name the
// manager has never started.
func (m *Manager) Reconnect(name string, onDone func(name string, c *mcp.Client, err error)) bool {
	m.mu.Lock()
	spec, ok := m.launch[name]
	m.mu.Unlock()
	if !ok {
		return false
	}
	m.Stop(name)
	if onDone == nil {
		onDone = spec.onDone
	}
	return m.Start(spec.ctx, name, spec.cfg, onDone)
}

// toolsChanged re-registers a server's tools after it announced a new
// list. The old names go first: a tool the server dropped must not stay
// callable.
func (m *Manager) toolsChanged(c *mcp.Client) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients[c.Name] != c || m.Registry == nil {
		return
	}
	prefix := mcp.ToolName(c.Name, "")
	for _, name := range m.Registry.Names() {
		if strings.HasPrefix(name, prefix) {
			m.Registry.Remove(name)
		}
	}
	m.register(c)
}

// register adds a client's tools. A server with more tools than the agent
// wants in every request contributes them deferred, to be loaded on demand
// through tool_search. Called with mu held.
func (m *Manager) register(c *mcp.Client) {
	if m.Registry == nil || c == nil {
		return
	}
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

// unregister takes a client's tools back out of the registry. It is called
// without mu, since the registry has a lock of its own.
func (m *Manager) unregister(c *mcp.Client) {
	if m.Registry == nil || c == nil {
		return
	}
	for _, t := range AgentTools(c) {
		m.Registry.Remove(t.Name())
	}
}

// Stop closes one server and removes its tools, so Start can bring it
// back (after /mcp auth, for instance).
func (m *Manager) Stop(name string) {
	m.mu.Lock()
	m.init()
	c := m.clients[name]
	delete(m.clients, name)
	delete(m.errs, name)
	m.gen[name]++
	var stale *mcp.Client
	if st := m.down[name]; st != nil {
		if st.timer != nil {
			st.timer.Stop()
		}
		stale = st.client
		delete(m.down, name)
	}
	m.mu.Unlock()
	m.unregister(stale)
	if c == nil {
		return
	}
	m.unregister(c)
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
	for name, st := range m.down {
		if seen[name] {
			continue
		}
		seen[name] = true
		row := ServerStatus{Name: name, Err: m.errs[name], Down: true, Attempts: st.attempts, GaveUp: st.gaveUp, Starting: m.starting[name]}
		if !st.gaveUp {
			row.NextRetry = st.next
		}
		for _, t := range AgentTools(st.client) {
			row.Tools = append(row.Tools, t.Name())
		}
		out = append(out, row)
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

// Close stops every server and removes its tools. The manager stays
// closed: a start still shaking hands stops its own server when it
// finishes, and a new one is refused.
func (m *Manager) Close() {
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*mcp.Client{}
	m.closed = true
	var stale []*mcp.Client
	for _, st := range m.down {
		if st.timer != nil {
			st.timer.Stop()
		}
		stale = append(stale, st.client)
	}
	m.down = nil
	m.mu.Unlock()
	for _, c := range stale {
		m.unregister(c)
	}
	for _, c := range clients {
		m.unregister(c)
		c.Close()
	}
}
