// Package session persists conversations as append-only JSONL files so a
// crash never loses more than the in-flight turn.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Meta is the first line of a session file.
type Meta struct {
	ID       string    `json:"id"`
	Cwd      string    `json:"cwd"`
	Provider string    `json:"provider"`
	Model    string    `json:"model"`
	Created  time.Time `json:"created"`
}

type line struct {
	Type    string       `json:"type"`
	Meta    *Meta        `json:"meta,omitempty"`
	Message *llm.Message `json:"message,omitempty"`
}

// Session is one persisted conversation.
type Session struct {
	Meta Meta
	path string

	persisted  int // messages already on disk
	lastBlocks int // block count of the last persisted message
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

// New creates a session file in dir.
func New(dir, cwd, provider, model string) (*Session, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102-150405") + "-" + randSuffix()
	s := &Session{
		Meta: Meta{ID: id, Cwd: cwd, Provider: provider, Model: model, Created: time.Now().UTC()},
		path: filepath.Join(dir, id+".jsonl"),
	}
	if err := s.rewrite(nil); err != nil {
		return nil, err
	}
	return s, nil
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
	var msgs []llm.Message
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
		case "message":
			if l.Message != nil {
				msgs = append(msgs, *l.Message)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if s.Meta.ID == "" {
		return nil, nil, fmt.Errorf("session %s has no metadata", id)
	}
	s.persisted = len(msgs)
	if s.persisted > 0 {
		s.lastBlocks = len(msgs[s.persisted-1].Blocks)
	}
	return s, msgs, nil
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
	Preview  string // first user text, trimmed
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
		info := Info{Meta: s.Meta, Messages: len(msgs)}
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
	sort.Slice(out, func(i, j int) bool { return out[i].Meta.ID > out[j].Meta.ID })
	return out, nil
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

// Path is the file backing the session.
func (s *Session) Path() string { return s.path }

// Persisted reports how many messages are on disk.
func (s *Session) Persisted() int { return s.persisted }

// SyncOrRotate persists msgs. When the history shrank (compaction replaced
// it) the old file is left intact and a new session is started for the
// summarised conversation; the session to keep using is returned.
func (s *Session) SyncOrRotate(msgs []llm.Message) (*Session, error) {
	if len(msgs) < s.persisted {
		ns, err := New(filepath.Dir(s.path), s.Meta.Cwd, s.Meta.Provider, s.Meta.Model)
		if err != nil {
			return s, err
		}
		return ns, ns.Sync(msgs)
	}
	return s, s.Sync(msgs)
}

// Sync writes any messages not yet on disk. If the last persisted message
// grew (a user turn merged after a failed call) the file is rewritten.
func (s *Session) Sync(msgs []llm.Message) error {
	if len(msgs) < s.persisted || (s.persisted > 0 && len(msgs[s.persisted-1].Blocks) != s.lastBlocks) {
		return s.rewrite(msgs)
	}
	if len(msgs) == s.persisted {
		return nil
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, m := range msgs[s.persisted:] {
		if err := writeLine(w, line{Type: "message", Message: &m}); err != nil {
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
	s.persisted = len(msgs)
	s.lastBlocks = len(msgs[s.persisted-1].Blocks)
	return nil
}

func (s *Session) rewrite(msgs []llm.Message) error {
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
	for i := range msgs {
		if err := writeLine(w, line{Type: "message", Message: &msgs[i]}); err != nil {
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
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.persisted = len(msgs)
	s.lastBlocks = 0
	if len(msgs) > 0 {
		s.lastBlocks = len(msgs[len(msgs)-1].Blocks)
	}
	return nil
}

func writeLine(w *bufio.Writer, l line) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return w.WriteByte('\n')
}
