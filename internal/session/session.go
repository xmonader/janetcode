// Package session persists conversations as append-only JSONL files so a
// crash never loses more than the in-flight turn.
package session

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/llm"
)

// Meta is the first line of a session file.
type Meta struct {
	ID       string    `json:"id"`
	Cwd      string    `json:"cwd"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Created  time.Time `json:"created"`
	// Pins are user notes kept in the system prompt (/pin).
	Pins []string `json:"pins,omitempty"`
	// Title is a short name, set by the user or generated after the first turn.
	Title string `json:"title,omitempty"`
	// ForkOf is the session this one was branched from.
	ForkOf string `json:"fork_of,omitempty"`
	// Settled marks a session the user is done with; it lists after the
	// active ones. Any new event clears it.
	Settled bool `json:"settled,omitempty"`
}

type line struct {
	Type string `json:"type"`
	Meta *Meta  `json:"meta,omitempty"`
}

type eventLine struct {
	Type string `json:"type"`
	Event
}

// Session is one persisted conversation. The file is the event log: every
// model-visible message is an appended surface event, so nothing but the
// metadata line is ever rewritten. Append is called from the parallel tool
// goroutines of a turn, so the log is guarded by a mutex.
type Session struct {
	// unwritten is set until the first event or rewrite creates the file.
	unwritten bool
	Meta      Meta
	path      string

	mu      sync.Mutex
	ev      []Event
	nextSeq int
}

// Dir returns the sessions directory, honouring XDG_DATA_HOME.
func Dir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "janetcode", "sessions"), nil
}

// ArchiveDir is where full tool outputs for the project at cwd are kept,
// beside the sessions directory and shared by all of the project's sessions.
func ArchiveDir(sessionsDir, cwd string) string {
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Join(filepath.Dir(sessionsDir), "archive", hex.EncodeToString(sum[:8]))
}

// New creates a session file in dir.
func New(dir, cwd, provider, model string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + randSuffix()
	// The file is written with the first event, not here: a start that
	// never gets to a prompt — no key, a bad key, a TUI opened and closed
	// — would otherwise leave an empty session in the list every time.
	return &Session{
		Meta:      Meta{ID: id, Cwd: cwd, Provider: provider, Model: model, Created: time.Now().UTC()},
		path:      filepath.Join(dir, id+".jsonl"),
		unwritten: true,
	}, nil
}

func randSuffix() string {
	b := make([]byte, 3)
	f, err := os.Open("/dev/urandom")
	if err == nil {
		defer f.Close()
		_, _ = f.Read(b)
	}
	return fmt.Sprintf("%02x%02x%02x", b[0], b[1], b[2])
}

// Open loads an existing session by ID and returns its messages.
func Open(dir, id string) (*Session, []llm.Message, error) {
	if strings.ContainsAny(id, "/\\") || id == "" {
		return nil, nil, fmt.Errorf("invalid session id %q", id)
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	s := &Session{path: path}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l line
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			// A torn final line (crash mid-write) is dropped, not fatal.
			if sc.Scan() {
				return nil, nil, fmt.Errorf("session %s line %d: %w", id, n, err)
			}
			break
		}
		switch l.Type {
		case "meta":
			if l.Meta != nil {
				s.Meta = *l.Meta
			}
		case "event":
			var el eventLine
			if err := json.Unmarshal([]byte(raw), &el); err != nil {
				return nil, nil, fmt.Errorf("session %s line %d: %w", id, n, err)
			}
			e := el.Event
			if e.Seq > s.nextSeq {
				s.nextSeq = e.Seq
			}
			s.ev = append(s.ev, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if s.Meta.ID == "" {
		return nil, nil, fmt.Errorf("session %s has no metadata", id)
	}
	return s, FoldSurface(s.ev), nil
}

// Latest returns the most recent session ID recorded for cwd, or "" if none.
func Latest(dir, cwd string) (string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, strings.TrimSuffix(e.Name(), ".jsonl"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	// The newest session with a conversation in it. A newer one that
	// never got past its first prompt (a goal set and abandoned, a start
	// that was quit) would otherwise be what --continue lands in, with
	// nothing to continue.
	empty := ""
	for _, id := range names {
		s, msgs, err := Open(dir, id)
		if err != nil || s.Meta.Cwd != cwd {
			continue
		}
		if len(msgs) > 0 {
			return id, nil
		}
		if empty == "" {
			empty = id
		}
	}
	if empty != "" {
		return empty, nil
	}
	for _, id := range names {
		s, _, err := Open(dir, id)
		if err != nil {
			continue
		}
		if s.Meta.Cwd == cwd {
			return id, nil
		}
	}
	return "", nil
}

// Info summarises a stored session.
type Info struct {
	Meta     Meta
	Messages int
	Preview  string    // first user text, trimmed
	Updated  time.Time // last activity, from the file
}

// Label is the title when set, else the preview, else the id.
func (in Info) Label() string {
	switch {
	case in.Meta.Title != "":
		return in.Meta.Title
	case in.Preview != "":
		return in.Preview
	}
	return in.Meta.ID
}

// List returns the sessions recorded for cwd (all when cwd is ""), newest
// first. Unreadable files are skipped.
func List(dir, cwd string) ([]Info, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		s, msgs, err := Open(dir, strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil || (cwd != "" && s.Meta.Cwd != cwd) {
			continue
		}
		info := Info{Meta: s.Meta, Messages: len(msgs), Updated: s.Meta.Created}
		if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil {
			info.Updated = st.ModTime()
		}
		for _, m := range msgs {
			if m.Role == llm.RoleUser {
				if t := strings.TrimSpace(m.Text()); t != "" {
					info.Preview = firstLine(t, 60)
					break
				}
			}
		}
		out = append(out, info)
	}
	// Active sessions first, most recently touched first; settled ones
	// after them. A session someone comes back to un-settles itself and
	// rises again.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Meta.Settled != out[j].Meta.Settled {
			return !out[i].Meta.Settled
		}
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return newer(out[i].Meta, out[j].Meta)
	})
	return out, nil
}

// SetSettled marks the session done with, or active again.
func (s *Session) SetSettled(v bool) error {
	s.mu.Lock()
	s.Meta.Settled = v
	s.mu.Unlock()
	return s.rewrite()
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// UndoPath is where the session's undo checkpoints live.
func (s *Session) UndoPath() string { return strings.TrimSuffix(s.path, ".jsonl") + ".undo.json" }

// Path is the file backing the session.
func (s *Session) Path() string { return s.path }

// Events is a snapshot of the append-only log, including shadowed and
// log-only records. The copy keeps readers safe from a concurrent Append.
func (s *Session) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.ev...)
}

// Append writes one event and returns the seq it was given. Seq is assigned
// if zero. Prior events are never rewritten.
func (s *Session) Append(e Event) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unwritten {
		if err := s.rewriteLocked(); err != nil {
			return 0, err
		}
	}
	if s.Meta.Settled {
		// Activity un-settles: the session is live again.
		s.Meta.Settled = false
		if err := s.rewriteLocked(); err != nil {
			return 0, err
		}
	}
	if e.Seq == 0 {
		s.nextSeq++
		e.Seq = s.nextSeq
	} else if e.Seq > s.nextSeq {
		s.nextSeq = e.Seq
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	w := bufio.NewWriter(f)
	if err := writeLine(w, eventLine{Type: "event", Event: e}); err != nil {
		f.Close()
		return 0, err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	s.ev = append(s.ev, e)
	return e.Seq, nil
}

// SetTitle stores a title in the metadata line and rewrites the file.
func (s *Session) SetTitle(title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Meta.Title = strings.TrimSpace(title)
	return s.rewriteLocked()
}

// Fork starts a new session holding a copy of this one's messages, so a
// conversation can branch: try one approach here, another in the fork.
func Fork(dir string, from *Session, title string) (*Session, error) {
	ns, err := New(dir, from.Meta.Cwd, from.Meta.Provider, from.Meta.Model)
	if err != nil {
		return nil, err
	}
	ns.Meta.Pins = append([]string(nil), from.Meta.Pins...)
	ns.Meta.ForkOf = from.Meta.ID
	if title = strings.TrimSpace(title); title == "" {
		title = "fork of " + from.Label()
	}
	ns.Meta.Title = title
	ns.ev = from.Events()
	for _, e := range ns.ev {
		if e.Seq > ns.nextSeq {
			ns.nextSeq = e.Seq
		}
	}
	if err := ns.rewrite(); err != nil {
		return nil, err
	}
	return ns, nil
}

// Label is the title, or the id when there is none.
func (s *Session) Label() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Meta.Title != "" {
		return s.Meta.Title
	}
	return s.Meta.ID
}

// AutoTitle derives a title from the first user message: its first line,
// trimmed to a few words. It is the fallback when no model wrote one.
func AutoTitle(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		t := strings.TrimSpace(m.Text())
		if t == "" || strings.HasPrefix(t, "[") || strings.HasPrefix(t, "<") {
			continue
		}
		words := strings.Fields(firstLine(t, 200))
		if len(words) > 8 {
			words = words[:8]
		}
		return strings.TrimRight(strings.Join(words, " "), ".,;:")
	}
	return ""
}

// Hit is one search match in a saved session.
type Hit struct {
	Info    Info
	Role    llm.Role
	Snippet string
	Matches int
}

// Search scans every session (all directories when cwd is "") for
// messages containing every word of the query, case-insensitively, and
// returns the best snippet per session, newest first.
func Search(dir, cwd, query string, limit int) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hits []Hit
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		s, msgs, err := Open(dir, strings.TrimSuffix(e.Name(), ".jsonl"))
		if err != nil || (cwd != "" && s.Meta.Cwd != cwd) {
			continue
		}
		var best Hit
		for _, m := range msgs {
			for _, b := range m.Blocks {
				if b.Type != llm.BlockText && b.Type != llm.BlockToolResult {
					continue
				}
				low := strings.ToLower(b.Text)
				n := 0
				first := -1
				for _, w := range words {
					i := strings.Index(low, w)
					if i < 0 {
						n = 0
						break
					}
					n++
					if first < 0 || i < first {
						first = i
					}
				}
				if n == 0 {
					continue
				}
				score := n
				if b.Type == llm.BlockText {
					score++ // conversation beats tool output
				}
				if score > best.Matches {
					best = Hit{Role: m.Role, Snippet: snippetAround(b.Text, first), Matches: score}
				}
			}
		}
		if best.Matches == 0 {
			continue
		}
		best.Info = Info{Meta: s.Meta, Messages: len(msgs)}
		hits = append(hits, best)
	}
	sort.Slice(hits, func(i, j int) bool { return newer(hits[i].Info.Meta, hits[j].Info.Meta) })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// newer orders sessions newest first; ids made in the same second differ
// only by their random suffix, so the creation time decides.
func newer(a, b Meta) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.After(b.Created)
	}
	return a.ID > b.ID
}

// snippetAround returns the line of text around byte offset at, which the
// caller found in a lowercased copy of it.
//
// Lowercasing is not length-preserving: Ⱥ and Ⱦ each gain a byte, so a
// message containing one puts the offset past the end of the original and
// every slice below panics. The offset is therefore clamped and pulled
// back to a rune boundary before anything is cut with it — a snippet a
// couple of bytes off centre beats taking the whole search down.
func snippetAround(text string, at int) string {
	if at > len(text) {
		at = len(text)
	}
	if at < 0 {
		at = 0
	}
	for at > 0 && at < len(text) && !utf8.RuneStart(text[at]) {
		at--
	}
	start := strings.LastIndexByte(text[:at], '\n') + 1
	end := strings.IndexByte(text[at:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += at
	}
	line := strings.TrimSpace(text[start:end])
	if len(line) > 120 {
		from := at - start - 40
		if from < 0 {
			from = 0
		}
		if from+120 > len(line) {
			from = len(line) - 120
		}
		to := from + 120
		for from > 0 && !utf8.RuneStart(line[from]) {
			from--
		}
		for to < len(line) && !utf8.RuneStart(line[to]) {
			to++
		}
		line = "…" + line[from:to] + "…"
	}
	return line
}

// SavePins stores the pins in the metadata line and rewrites the file.
func (s *Session) SavePins(pins []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Meta.Pins = append([]string(nil), pins...)
	return s.rewriteLocked()
}

// rewrite replaces the file with the metadata line followed by every event
// held in memory. Only metadata changes need it; events are appended.
func (s *Session) rewrite() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rewriteLocked()
}

// rewriteLocked is rewrite for a caller already holding s.mu.
func (s *Session) rewriteLocked() error {
	s.unwritten = false
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	if err := writeLine(w, line{Type: "meta", Meta: &s.Meta}); err != nil {
		f.Close()
		return err
	}
	for i := range s.ev {
		if err := writeLine(w, eventLine{Type: "event", Event: s.ev[i]}); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func writeLine(w *bufio.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}
