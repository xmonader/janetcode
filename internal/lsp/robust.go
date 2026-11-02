package lsp

// Robustness around the server connection, after grok's lsp/{pull,
// watched_files,restart,pending}.rs: pull diagnostics when the server
// offers them, tell the server about files changed outside the editor
// buffer, restart a crashed server with backoff and its open documents
// replayed, and never let a silent server hold a turn.

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// defaultCallTimeout backstops a request whose caller set no
	// deadline of its own.
	defaultCallTimeout = 30 * time.Second
	// maxStalls is how many requests in a row may time out with no
	// answer of any kind before the server is written off as hung and
	// restarted. Any response, or any diagnostics, resets the count.
	maxStalls = 3
	// restartBackoff is the wait before the first restart; it doubles.
	restartBackoff = time.Second
	// stableAfter is how long a restarted server must stay up before
	// its crashes stop counting against it.
	stableAfter = 2 * time.Minute
	// maxReplay bounds how many open documents are re-sent to a restarted
	// server.
	maxReplay = 50
	// maxQueuedEvents bounds file events held before the server has
	// registered its watchers.
	maxQueuedEvents = 256
)

// Change types of workspace/didChangeWatchedFiles.
const (
	Created = 1
	Changed = 2
	Deleted = 3
)

// FileEvent is one file that changed outside the server's buffers.
type FileEvent struct {
	Path string // absolute
	Type int
}

// --- pending-request tracking ---

// touch records that the server said something, which ends a run of
// stalls.
func (c *client) touch() { atomic.StoreInt32(&c.stalls, 0) }

// stall records a request that went unanswered.
func (c *client) stall() { atomic.AddInt32(&c.stalls, 1) }

// hung reports a server that is running but has stopped answering.
func (c *client) hung() bool { return atomic.LoadInt32(&c.stalls) >= maxStalls }

// usable is alive and answering.
func (c *client) usable() bool { return c.alive() && !c.hung() }

// pending is how many requests await an answer.
func (c *client) pending() int {
	c.pendM.Lock()
	defer c.pendM.Unlock()
	return len(c.pend)
}

// --- pull diagnostics (textDocument/diagnostic) ---

// readCaps notes what the server said it can do.
func (c *client) readCaps(result json.RawMessage) {
	var r struct {
		Capabilities struct {
			DiagnosticProvider json.RawMessage `json:"diagnosticProvider"`
		} `json:"capabilities"`
	}
	if json.Unmarshal(result, &r) != nil {
		return
	}
	p := strings.TrimSpace(string(r.Capabilities.DiagnosticProvider))
	c.pull = p != "" && p != "null" && p != "false"
}

// pullDiagnostics asks the server for the file's diagnostics. ok is false
// when it could not answer (the caller then waits for a push instead).
func (c *client) pullDiagnostics(ctx context.Context, uri string, version int) (items []Diagnostic, ok bool) {
	c.docM.Lock()
	params := map[string]any{"textDocument": map[string]any{"uri": uri}}
	if id := c.resultIDs[uri]; id != "" {
		params["previousResultId"] = id
	}
	c.docM.Unlock()
	raw, err := c.call(ctx, "textDocument/diagnostic", params)
	if err != nil {
		return nil, false
	}
	var r struct {
		Kind     string       `json:"kind"`
		ResultID string       `json:"resultId"`
		Items    []Diagnostic `json:"items"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil, false
	}
	c.docM.Lock()
	defer c.docM.Unlock()
	st := c.diags[uri]
	if r.Kind != "unchanged" {
		st.items = r.Items
	}
	st.version, st.seq = version, st.seq+1
	c.diags[uri] = st
	if r.ResultID != "" {
		if c.resultIDs == nil {
			c.resultIDs = map[string]string{}
		}
		c.resultIDs[uri] = r.ResultID
	}
	return st.items, true
}

// --- workspace/didChangeWatchedFiles ---

// watcher is one registered file watch: a glob, optionally relative to a
// base directory, for the event kinds in kind (1 create, 2 change, 4
// delete; 7 when the server named none).
type watcher struct {
	re   *regexp.Regexp
	base string // "" matches the absolute path
	kind int
}

// registration is the id the server gave a client/registerCapability
// entry and the watchers it carried, so unregister can drop them.
type registration struct {
	id       string
	watchers []watcher
}

// register handles client/registerCapability.
func (c *client) register(params json.RawMessage) {
	var p struct {
		Registrations []struct {
			ID              string `json:"id"`
			Method          string `json:"method"`
			RegisterOptions struct {
				Watchers []struct {
					GlobPattern json.RawMessage `json:"globPattern"`
					Kind        *int            `json:"kind"`
				} `json:"watchers"`
			} `json:"registerOptions"`
		} `json:"registrations"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	var flush []FileEvent
	for _, r := range p.Registrations {
		if r.Method != "workspace/didChangeWatchedFiles" {
			continue
		}
		reg := registration{id: r.ID}
		for _, w := range r.RegisterOptions.Watchers {
			pat, base := "", ""
			var s string
			if json.Unmarshal(w.GlobPattern, &s) == nil {
				pat = s
			} else {
				var rel struct {
					BaseURI json.RawMessage `json:"baseUri"`
					Pattern string          `json:"pattern"`
				}
				if json.Unmarshal(w.GlobPattern, &rel) != nil {
					continue
				}
				pat = rel.Pattern
				var u string
				if json.Unmarshal(rel.BaseURI, &u) != nil {
					var wf struct {
						URI string `json:"uri"`
					}
					_ = json.Unmarshal(rel.BaseURI, &wf)
					u = wf.URI
				}
				base = fromURI(u)
			}
			re, err := globRegexp(pat)
			if err != nil {
				continue
			}
			kind := 7
			if w.Kind != nil {
				kind = *w.Kind
			}
			reg.watchers = append(reg.watchers, watcher{re: re, base: base, kind: kind})
		}
		c.watchM.Lock()
		c.regs = append(c.regs, reg)
		c.watchM.Unlock()
	}
	c.watchM.Lock()
	if c.hasWatchers() {
		flush, c.queued = c.queued, nil
	}
	c.watchM.Unlock()
	if len(flush) > 0 {
		c.sendWatched(flush)
	}
}

// unregister handles client/unregisterCapability.
func (c *client) unregister(params json.RawMessage) {
	var p struct {
		Unregisterations []struct {
			ID string `json:"id"`
		} `json:"unregisterations"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	c.watchM.Lock()
	defer c.watchM.Unlock()
	for _, u := range p.Unregisterations {
		for i, r := range c.regs {
			if r.id == u.ID {
				c.regs = append(c.regs[:i], c.regs[i+1:]...)
				break
			}
		}
	}
}

// hasWatchers is called with watchM held.
func (c *client) hasWatchers() bool {
	for _, r := range c.regs {
		if len(r.watchers) > 0 {
			return true
		}
	}
	return false
}

func (w watcher) matches(path string, typ int) bool {
	if w.kind&(1<<(typ-1)) == 0 {
		return false
	}
	if w.base == "" {
		return w.re.MatchString(filepath.ToSlash(path))
	}
	rel, err := filepath.Rel(w.base, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	return w.re.MatchString(filepath.ToSlash(rel))
}

// watched sends the events some registered watcher wants; before the
// server has registered any, events are held (bounded) for when it does.
func (c *client) watched(evs []FileEvent) {
	c.watchM.Lock()
	if !c.hasWatchers() {
		c.queued = append(c.queued, evs...)
		if n := len(c.queued); n > maxQueuedEvents {
			c.queued = c.queued[n-maxQueuedEvents:]
		}
		c.watchM.Unlock()
		return
	}
	var send []FileEvent
	for _, e := range evs {
	next:
		for _, r := range c.regs {
			for _, w := range r.watchers {
				if w.matches(e.Path, e.Type) {
					send = append(send, e)
					break next
				}
			}
		}
	}
	c.watchM.Unlock()
	c.sendWatched(send)
}

func (c *client) sendWatched(evs []FileEvent) {
	if len(evs) == 0 {
		return
	}
	changes := make([]map[string]any, len(evs))
	for i, e := range evs {
		changes[i] = map[string]any{"uri": toURI(e.Path), "type": e.Type}
	}
	ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
	defer cancel()
	_ = c.notifyCtx(ctx, "workspace/didChangeWatchedFiles", map[string]any{"changes": changes})
}

// globRegexp compiles an LSP glob: * (no separator), ? , ** (any depth),
// [a-z] / [!a-z] classes and {a,b} alternatives.
func globRegexp(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	braces := 0
	for i := 0; i < len(glob); i++ {
		ch := glob[i]
		switch ch {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					sb.WriteString("(?:.*/)?")
				} else {
					sb.WriteString(".*")
				}
			} else {
				sb.WriteString("[^/]*")
			}
		case '?':
			sb.WriteString("[^/]")
		case '{':
			braces++
			sb.WriteString("(?:")
		case '}':
			if braces > 0 {
				braces--
				sb.WriteString(")")
			} else {
				sb.WriteString(`\}`)
			}
		case ',':
			if braces > 0 {
				sb.WriteString("|")
			} else {
				sb.WriteString(",")
			}
		case '[':
			end := strings.IndexByte(glob[i:], ']')
			if end < 0 {
				sb.WriteString(`\[`)
				break
			}
			class := glob[i+1 : i+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			sb.WriteString("[" + class + "]")
			i += end
		default:
			sb.WriteString(regexp.QuoteMeta(string(ch)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// NotifyChanged tells every running server that files changed outside its
// buffers (a shell command, a formatter, a checkout), so it re-reads them
// instead of serving stale answers. Servers that registered no matching
// watcher are not bothered.
func (m *Manager) NotifyChanged(evs ...FileEvent) {
	if m == nil || m.Plain || len(evs) == 0 {
		return
	}
	for i := range evs {
		evs[i].Path = m.abs(evs[i].Path)
	}
	m.mu.Lock()
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.Unlock()
	for _, c := range clients {
		if c.usable() {
			c.watched(evs)
		}
	}
}

// fileStamp is what SyncFromDisk compares.
type fileStamp struct {
	mod  time.Time
	size int64
}

// SyncFromDisk finds the files that changed since the last call, by asking
// git which files are dirty and comparing their mtime and size with what
// it saw before, and passes them to NotifyChanged. The first call only
// records a baseline. It is bounded and quiet: outside git, or with no
// server running, it does nothing.
func (m *Manager) SyncFromDisk(ctx context.Context) {
	if m == nil || m.Plain {
		return
	}
	m.mu.Lock()
	running := len(m.clients) > 0
	m.mu.Unlock()
	if !running {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	root := m.root()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain", "-z", "--untracked-files=all").Output()
	if err != nil {
		return
	}
	now := map[string]fileStamp{}
	parts := strings.Split(string(out), "\x00")
	for i := 0; i < len(parts); i++ {
		e := parts[i]
		if len(e) < 4 {
			continue
		}
		if e[0] == 'R' || e[0] == 'C' {
			i++ // the rename's source follows
		}
		p := filepath.Join(root, e[3:])
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			now[p] = fileStamp{info.ModTime(), info.Size()}
		} else {
			now[p] = fileStamp{}
		}
	}
	m.mu.Lock()
	prev := m.stamps
	m.stamps = now
	m.mu.Unlock()
	if prev == nil {
		return
	}
	var evs []FileEvent
	for p, st := range now {
		old, seen := prev[p]
		switch {
		case st == (fileStamp{}):
			if !seen || old != (fileStamp{}) {
				evs = append(evs, FileEvent{p, Deleted})
			}
		case !seen:
			evs = append(evs, FileEvent{p, Created})
		case old != st:
			evs = append(evs, FileEvent{p, Changed})
		}
	}
	// A file that was dirty and no longer is changed back (reverted).
	for p := range prev {
		if _, still := now[p]; !still {
			evs = append(evs, FileEvent{p, Changed})
		}
	}
	m.NotifyChanged(evs...)
}

// --- restart with backoff and replay ---

// backoff is the wait before restart number n (1-based).
func (m *Manager) backoff(n int) time.Duration {
	base := m.RestartBackoff
	if base <= 0 {
		base = restartBackoff
	}
	return base << min(n-1, 6)
}

// openFiles lists the documents a client has open, for replay.
func (c *client) openFiles() []string {
	c.docM.Lock()
	defer c.docM.Unlock()
	out := make([]string, 0, len(c.docs))
	for uri := range c.docs {
		out = append(out, fromURI(uri))
	}
	return out
}

// replay re-sends documents a crashed server had open, so a restarted
// one knows the same files. Files that are gone are skipped.
func (c *client) replay(ctx context.Context, files []string) {
	if len(files) > maxReplay {
		files = files[:maxReplay]
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if c.sync(ctx, f, string(data)) != nil {
			return
		}
	}
}
