// Package lsp talks to language servers (gopls, pyright, tsserver, …) over
// stdio so the agent sees real compiler diagnostics right after it edits a
// file, instead of discovering them a build later. Only the parts of the
// protocol needed for that are implemented: initialize, didOpen/didChange
// with full text, and publishDiagnostics. Servers start lazily on the
// first edit of a matching file and a missing binary is remembered so it
// is not retried every turn.
package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Diagnostic is one reported problem.
type Diagnostic struct {
	Range struct {
		Start Position `json:"start"`
		End   Position `json:"end"`
	} `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

// Position is zero-based in the protocol.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// SeverityName maps the protocol's severity to a word.
func SeverityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return "error"
}

// Format renders one diagnostic as path:line:col: severity: message.
func (d Diagnostic) Format(path string) string {
	src := ""
	if d.Source != "" {
		src = " (" + d.Source + ")"
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s%s", path, d.Range.Start.Line+1, d.Range.Start.Character+1, SeverityName(d.Severity), strings.TrimSpace(d.Message), src)
}

// Manager owns one client per server, started on demand.
type Manager struct {
	Root    string
	Config  Config
	Log     func(string)
	mu      sync.Mutex
	clients map[string]*client // by server name, once handshaken
	// starting holds the servers whose handshake is still in flight, so a
	// second caller waits for it instead of starting a second server.
	starting map[string]*pending
	failed   map[string]string
	// restarts counts how often a server has been started again after
	// dying, so one that keeps crashing is given up on.
	restarts map[string]int
	// retryAt is when a crashed server may be started again (backoff);
	// startedAt when the running one came up, so a stable run clears the
	// crash count; replay holds the documents the dead one had open.
	retryAt   map[string]time.Time
	startedAt map[string]time.Time
	replay    map[string][]string
	// stamps is SyncFromDisk's record of dirty files.
	stamps map[string]fileStamp
	// RestartBackoff is the wait before a crashed server's first restart,
	// doubling each time; zero means a second.
	RestartBackoff time.Duration
	// Plain turns off pull diagnostics, watched-file notifications and
	// hung-server detection (JANETCODE_OFF=lspsupervise), leaving the
	// push-only client with lazy restarts.
	Plain bool
	byExt map[string]string // extension → server name
	// life is cancelled by Close, which is what ends a handshake that is
	// still running. closed says the manager is gone, so a start that
	// finishes after it is stopped rather than registered as a server
	// nothing will ever close.
	life   context.Context
	stop   context.CancelFunc
	closed bool
}

// pending is one handshake in flight. ready is closed once c and err are
// set, and everything else waiting on that server reads them then.
type pending struct {
	ready chan struct{}
	c     *client
	err   error
}

// New prepares a manager; nothing starts until Diagnose is called.
func New(root string, cfg Config) *Manager {
	m := &Manager{Root: root, Config: cfg, clients: map[string]*client{}, starting: map[string]*pending{},
		failed: map[string]string{}, restarts: map[string]int{}, retryAt: map[string]time.Time{}, startedAt: map[string]time.Time{}, replay: map[string][]string{}, byExt: map[string]string{}}
	m.life, m.stop = context.WithCancel(context.Background())
	for name, s := range cfg.Active() {
		for _, e := range s.Extensions {
			m.byExt[strings.ToLower(e)] = name
		}
	}
	return m
}

// Enabled reports whether diagnostics may run at all.
func (m *Manager) Enabled() bool { return m != nil && !m.Config.Disabled && len(m.byExt) > 0 }

// ServerFor names the server that handles the path, or "".
func (m *Manager) ServerFor(path string) string {
	if m == nil {
		return ""
	}
	return m.byExt[strings.ToLower(filepath.Ext(path))]
}

// Diagnose tells the server the file changed and returns its diagnostics
// for that file, formatted one per line, or "" when no server handles the
// file or none answered in time. A nil error with an empty result means
// the server answered with no problems; the "ok" flag separates the two.
func (m *Manager) Diagnose(ctx context.Context, path string) (lines []string, ok bool, err error) {
	if !m.Enabled() {
		return nil, false, nil
	}
	c, err := m.clientFor(ctx, path)
	if err != nil || c == nil {
		return nil, false, err
	}
	abs := m.abs(path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false, err
	}
	timeout := time.Duration(m.Config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ds, ok, err := c.diagnose(ctx, abs, string(data), timeout)
	if err != nil {
		return nil, false, err
	}
	return m.format(abs, ds), ok, nil
}

// Known returns the latest diagnostics for every file a server reported,
// formatted and sorted by path.
func (m *Manager) Known() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.Unlock()
	var out []string
	for _, c := range clients {
		for uri, ds := range c.snapshot() {
			out = append(out, m.format(fromURI(uri), ds)...)
		}
	}
	sort.Strings(out)
	return out
}

// Status describes each configured server: running, not installed, or idle.
func (m *Manager) Status() []string {
	if m == nil {
		return nil
	}
	servers := m.Config.Active()
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	// Copy what the lock protects and let it go: what follows looks at
	// PATH and at each client's own state, neither of which this lock
	// covers, and /lsp used to hold it across all of that.
	m.mu.Lock()
	failed := make(map[string]string, len(m.failed))
	for k, v := range m.failed {
		failed[k] = v
	}
	running := make(map[string]*client, len(m.clients))
	for k, v := range m.clients {
		running[k] = v
	}
	m.mu.Unlock()
	var out []string
	for _, n := range names {
		s := servers[n]
		state := "idle (starts on the first " + strings.Join(s.Extensions, "/") + " edit)"
		if why, bad := failed[n]; bad {
			state = "unavailable: " + why
		} else if c, up := running[n]; up {
			state = fmt.Sprintf("running (pid %d, %d files open)", c.cmd.Process.Pid, c.openCount())
			if c.hung() {
				state = fmt.Sprintf("not answering (pid %d, %d requests pending); restarts on the next edit", c.cmd.Process.Pid, c.pending())
			}
		} else if _, err := exec.LookPath(s.Command[0]); err != nil {
			state = "not installed (" + s.Command[0] + " not on PATH)"
		}
		out = append(out, n+": "+state)
	}
	if m.Config.Disabled {
		out = append([]string{"diagnostics are disabled in the config (\"lsp\": {\"disabled\": true})"}, out...)
	}
	return out
}

// Close stops every server, including one still shaking hands: a cold
// rust-analyzer takes the better part of a minute to answer initialize,
// and a session that ended inside that window used to leave it running.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*client{}
	m.closed = true
	stop := m.stop
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	for _, c := range clients {
		c.close()
	}
}

func (m *Manager) abs(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(m.root(), path)
}

// root is the project directory. It is read under the lock because /cd
// moves it while queries may be in flight.
func (m *Manager) root() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Root
}

// Reroot points the manager at another project and reports the servers it
// had to stop to do it. A language server cannot be told to change its
// root — the protocol has no message for it, and its index was built for
// the tree it was started on — so a running one is stopped here and the
// next query starts a fresh one in the new tree. The per-server failure
// memory is cleared with it: a server that is missing for a Go project may
// be exactly the one the next project needs.
func (m *Manager) Reroot(dir string) []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	clients := m.clients
	m.clients = map[string]*client{}
	m.failed = map[string]string{}
	m.restarts = map[string]int{}
	m.retryAt = map[string]time.Time{}
	m.replay = map[string][]string{}
	m.stamps = nil
	m.Root = dir
	stop := m.stop
	// A fresh lifetime, because the old one is about to be cancelled to
	// end any handshake still in flight against the directory we left.
	m.life, m.stop = context.WithCancel(context.Background())
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	names := make([]string, 0, len(clients))
	for name, c := range clients {
		names = append(names, name)
		c.close()
	}
	sort.Strings(names)
	return names
}

func (m *Manager) format(abs string, ds []Diagnostic) []string {
	rel := abs
	if r, err := filepath.Rel(m.root(), abs); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Format(rel))
	}
	return out
}

// clientFor returns the server for the path, starting it if it is not up.
//
// The lock covers the maps and nothing else. It used to be held across
// start(), whose initialize call is allowed thirty seconds: a cold
// rust-analyzer or clangd indexing a large tree froze every other caller
// for that whole time — /lsp would not answer, and the next edit's
// diagnostics queued behind a handshake they had nothing to do with. A
// start in flight is a pending entry in the map instead, and anyone else
// who wants that server waits on it, on their own context.
func (m *Manager) clientFor(ctx context.Context, path string) (*client, error) {
	name := m.ServerFor(path)
	if name == "" {
		return nil, nil
	}
	m.mu.Lock()
	if c, ok := m.clients[name]; ok {
		// A server that has died must not be handed out again: every
		// query against it fails with its exit status, which reads as a
		// fact about the code rather than as a crashed process. It is
		// dropped and started fresh, once — a server that keeps dying is
		// remembered as failed rather than restarted forever.
		//
		// One that is running but has stopped answering (several requests
		// in a row timed out with nothing back) is stopped and treated
		// the same way, so a hung server cannot hold up every turn.
		if c.alive() && (m.Plain || !c.hung()) {
			if time.Since(m.startedAt[name]) > stableAfter {
				m.restarts[name] = 0
			}
			m.mu.Unlock()
			return c, nil
		}
		why := "exited"
		if c.alive() {
			why = "stopped answering"
			go c.close()
		}
		delete(m.clients, name)
		m.replay[name] = c.openFiles()
		m.restarts[name]++
		if m.restarts[name] > maxRestarts {
			m.failed[name] = fmt.Sprintf("%s %d times; not started again this session", why, m.restarts[name])
			m.mu.Unlock()
			return nil, nil
		}
		m.retryAt[name] = time.Now().Add(m.backoff(m.restarts[name]))
	}
	// A crashed server is left alone until its backoff has passed: the
	// query goes without diagnostics rather than into a crash loop.
	if at, ok := m.retryAt[name]; ok && time.Now().Before(at) {
		m.mu.Unlock()
		return nil, nil
	}
	if _, bad := m.failed[name]; bad {
		m.mu.Unlock()
		return nil, nil
	}
	if p, ok := m.starting[name]; ok {
		m.mu.Unlock()
		select {
		case <-p.ready:
			return p.c, p.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	p := &pending{ready: make(chan struct{})}
	m.starting[name] = p
	command := m.Config.Active()[name].Command
	life := m.life
	m.mu.Unlock()

	// The handshake runs on the manager's own context, not the caller's:
	// one caller giving up on a slow server must not kill the server
	// everyone else is waiting for. Close is what cancels it.
	if life == nil {
		life = context.Background()
	}
	c, err := start(life, m.root(), command, m.Log)

	m.mu.Lock()
	delete(m.starting, name)
	switch {
	case m.life != life:
		// /cd moved the project while this was shaking hands, and
		// cancelling the old lifetime is what ended it. That is not a
		// verdict on the server — recording it as failed left the project
		// just moved into with a language server permanently
		// "unavailable: context canceled" — so the next query simply
		// starts a fresh one in the new tree.
		if c != nil {
			defer c.close()
		}
	case err != nil:
		m.failed[name] = err.Error()
		p.err = fmt.Errorf("lsp: %s: %w", name, err)
	case m.closed:
		// Close ran while this was shaking hands, so the server is ours
		// to stop: nothing gets a client out of a closed manager.
		defer c.close()
	default:
		if m.Plain {
			c.pull = false
		}
		m.clients[name] = c
		m.startedAt[name] = time.Now()
		delete(m.retryAt, name)
		p.c = c
		if m.stamps == nil {
			go m.SyncFromDisk(life) // the baseline later shell changes are measured against
		}
		if files := m.replay[name]; len(files) > 0 {
			delete(m.replay, name)
			go func() {
				ctx, cancel := context.WithTimeout(life, 10*time.Second)
				defer cancel()
				c.replay(ctx, files)
			}()
		}
	}
	m.mu.Unlock()
	close(p.ready)
	return p.c, p.err
}
