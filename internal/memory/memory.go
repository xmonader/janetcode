// Package memory keeps a short list of durable facts about a project that
// is read at the start of every session: conventions the user asked for,
// decisions and why, commands that work, environment quirks. It is keyed
// by project identity so it survives clones and moves.
package memory

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// MarkerFile, when present in the project, pins its identity to the
// file's content so several checkouts share one memory.
const MarkerFile = ".janetcode/project"

// MaxEntries bounds the file; the oldest entries are dropped past it.
const MaxEntries = 100

// MaxEntryLen bounds one entry.
const MaxEntryLen = 300

// Key returns the project identity: the marker file's content, else the
// git origin URL, else a hash of the path. The result is safe as a file
// name.
func Key(cwd string) string {
	if data, err := os.ReadFile(filepath.Join(cwd, MarkerFile)); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return slug(id)
		}
	}
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = cwd
	if out, err := cmd.Output(); err == nil {
		if u := normalizeRemote(strings.TrimSpace(string(out))); u != "" {
			return slug(u)
		}
	}
	sum := sha256.Sum256([]byte(cwd))
	return filepath.Base(cwd) + "-" + hex.EncodeToString(sum[:6])
}

// normalizeRemote reduces the ways of writing one remote to a single form:
// host/owner/repo without scheme, user, port or .git.
func normalizeRemote(u string) string {
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.Index(u, "@"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.Replace(u, ":", "/", 1)
	u = strings.TrimSuffix(u, "/")
	return strings.ToLower(u)
}

var unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func slug(s string) string {
	s = strings.Trim(unsafeRe.ReplaceAllString(s, "-"), "-.")
	if len(s) > 80 {
		sum := sha256.Sum256([]byte(s))
		s = s[:60] + "-" + hex.EncodeToString(sum[:4])
	}
	if s == "" {
		return "project"
	}
	return s
}

// Store is one project's memory file.
type Store struct {
	Path string
	Key  string

	mu sync.Mutex
}

// Open returns the store for the project at cwd under dataDir/memory.
func Open(dataDir, cwd string) *Store {
	key := Key(cwd)
	return &Store{Path: filepath.Join(dataDir, "memory", key+".md"), Key: key}
}

// Entries returns the remembered facts, oldest first.
func (s *Store) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read()
}

func (s *Store) read() []string {
	f, err := os.Open(s.Path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 64*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "- ") {
			out = append(out, strings.TrimSpace(line[2:]))
		}
	}
	return out
}

func (s *Store) write(entries []string) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "# janetcode memory for %s\n", s.Key)
	for _, e := range entries {
		sb.WriteString("- " + e + "\n")
	}
	return tool.AtomicWrite(s.Path, []byte(sb.String()))
}

// Clean normalises an entry to one trimmed line, or "" if it is empty.
func Clean(text string) string {
	text = strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(text), "- ")), " ")
	if len(text) > MaxEntryLen {
		text = text[:MaxEntryLen-1] + "…"
	}
	return text
}

// Add remembers text unless an equivalent entry exists. It reports whether
// anything was added.
func (s *Store) Add(text string) (bool, error) {
	text = Clean(text)
	if text == "" {
		return false, errors.New("nothing to remember")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.read()
	for _, e := range entries {
		if strings.EqualFold(e, text) {
			return false, nil
		}
	}
	entries = append(entries, text)
	if len(entries) > MaxEntries {
		entries = entries[len(entries)-MaxEntries:]
	}
	return true, s.write(entries)
}

// Remove forgets entry number n (1-based, as listed) and returns it.
func (s *Store) Remove(n int) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries := s.read()
	if n < 1 || n > len(entries) {
		return "", fmt.Errorf("no memory entry %d (there are %d)", n, len(entries))
	}
	gone := entries[n-1]
	entries = append(entries[:n-1], entries[n:]...)
	return gone, s.write(entries)
}

// Section renders the memory for the system prompt, or "" when empty.
func (s *Store) Section() string {
	entries := s.Entries()
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n# Project memory\nFacts remembered from earlier sessions on this project (the user can edit them with /memory, /remember and /forget). Follow them unless the user says otherwise.\n")
	for _, e := range entries {
		sb.WriteString("- " + e + "\n")
	}
	return sb.String()
}

const extractSystem = `You maintain a short memory file for a coding project. It is read at the start of every future session, so it must hold only durable facts about THIS project that a future session would need and could not learn in a minute from the code or git history: conventions the user asked for, decisions and the reasons behind them, commands that are known to work or to fail, environment quirks, things the user told you not to do. Record what this session DECIDED, not what it did. Never record task progress, transient state, file contents, or anything already in the existing memory. Output one fact per line, each a complete sentence under 200 characters, with no bullets, numbering or preamble. Output exactly NONE when there is nothing worth keeping.`

// extractWindow is how many recent messages the extractor sees.
const extractWindow = 14

// Extract asks the model which new durable facts the recent conversation
// established. Tool results are trimmed so the call stays cheap.
func Extract(ctx context.Context, p llm.Provider, model string, msgs []llm.Message, existing []string) ([]string, error) {
	if len(msgs) > extractWindow {
		msgs = msgs[len(msgs)-extractWindow:]
	}
	var slim []llm.Message
	for _, m := range msgs {
		c := llm.Message{Role: m.Role}
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockThinking:
				continue
			case llm.BlockToolResult:
				b.Text = tool.TruncateTo(b.Text, 600)
			}
			c.Blocks = append(c.Blocks, b)
		}
		if len(c.Blocks) > 0 {
			slim = append(slim, c)
		}
	}
	if len(slim) == 0 || slim[0].Role != llm.RoleUser {
		slim = append([]llm.Message{llm.TextMessage(llm.RoleUser, "(earlier conversation omitted)")}, slim...)
	}
	var ask strings.Builder
	ask.WriteString("Existing memory:\n")
	if len(existing) == 0 {
		ask.WriteString("(empty)\n")
	}
	for _, e := range existing {
		ask.WriteString("- " + e + "\n")
	}
	ask.WriteString("\nList the new durable facts now, or NONE.")
	slim = append(slim, llm.TextMessage(llm.RoleUser, ask.String()))
	resp, err := p.Stream(ctx, llm.Request{Model: model, System: extractSystem, Messages: slim, MaxTokens: 400}, nil)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(resp.Message.Text(), "\n") {
		line = Clean(strings.TrimLeft(strings.TrimSpace(line), "-*0123456789. "))
		if line == "" || strings.EqualFold(line, "none") || strings.EqualFold(line, "none.") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}
