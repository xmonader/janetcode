// Package session persists conversations as append-only JSONL files so a
// crash never loses more than the in-flight turn.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/xdg"
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
	// Loops are armed /loop jobs restored on resume of this session.
	Loops []LoopSpec `json:"loops,omitempty"`
	// ExtraDirs are additional local roots file tools may read and edit.
	ExtraDirs []string `json:"extra_dirs,omitempty"`
	// Start is the repository state recorded on this session's first user
	// turn, when its cwd is a git repository. /eval keep reads it to turn
	// the session into a regression eval that replays the exact tree and
	// commit the session began from.
	Start *StartState `json:"start,omitempty"`
	// Accept is the /accept acceptance-tests-you-approve-first state for
	// this session (see accept.go), so a resume restores the lock.
	Accept *AcceptMeta `json:"accept,omitempty"`
}

// StartState is a session's git state at its first turn: the commit HEAD
// pointed to and a snapshot commit (see internal/snapshot) of the whole
// work tree, tracked and untracked, at that moment.
type StartState struct {
	// Head is the commit sha HEAD pointed to; empty on an unborn branch.
	Head string `json:"head"`
	// Snap is the snapshot commit recording the work tree.
	Snap string    `json:"snap"`
	At   time.Time `json:"at"`
}

// LoopSpec is one persisted session loop (/loop).
type LoopSpec struct {
	ID     string `json:"id"`
	Every  string `json:"every"`
	Prompt string `json:"prompt"`
}

type line struct {
	Type string `json:"type"`
	Meta *Meta  `json:"meta,omitempty"`
}

type eventLine struct {
	Type string `json:"type"`
	Event
}

// record is a log line of either kind, so Open decodes each line once
// instead of parsing it as metadata and then again as an event. The outer
// Type is shallower than the embedded Event's own ("t"), so the two tags
// do not collide. Only Open uses it; writing keeps the narrow types, whose
// fields are exactly what each kind of line carries.
type record struct {
	Type string `json:"type"`
	Meta *Meta  `json:"meta,omitempty"`
	Event
}

// Session is one persisted conversation. The file is the event log: every
// model-visible message is an appended surface event, so nothing but the
// metadata line is ever rewritten. Append is called from the parallel tool
// goroutines of a turn, so the log is guarded by a mutex.
type Session struct {
	// unwritten is set until the first event or rewrite creates the file.
	unwritten bool
	// torn is set by Open when the file on disk ends in a partial line: a
	// crash or a power cut between the write and its newline. Those bytes
	// are dropped from the in-memory log, but they are still on disk, and
	// O_APPEND would glue the next event onto them into one corrupt line
	// — which is no longer the *last* line, so the next Open refuses the
	// whole session rather than dropping it. The next write rewrites the
	// file from memory instead, which drops the wreckage for good.
	torn bool
	Meta Meta
	path string

	mu      sync.Mutex
	ev      []Event
	nextSeq int
	// lastSync is when the log was last forced to durable storage; see
	// syncInterval.
	lastSync time.Time
	// leased is set while this Session holds the lease; see lease.go.
	leased bool
	// opened is how many events Open read from disk. The audit log
	// state (audit.go): whether it has been read, how many records it
	// has and the last one's hash, and how many of ev it covers.
	opened      int
	auditLoaded bool
	auditN      int
	auditHead   string
	audited     int
	// lastDigest is the digest of the event Append just wrote, from the
	// bytes it wrote, so the audit record does not marshal it again.
	lastDigest string
}

// Dir returns the sessions directory, honouring XDG_DATA_HOME.
func Dir() (string, error) {
	dir, err := xdg.DataDir(os.Getenv)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sessions"), nil
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
	// An id already on disk would be written over from scratch by this
	// session's first Append, taking the other conversation with it, so
	// the suffix is drawn again until the path is free.
	var id, path string
	for tries := 0; ; tries++ {
		suffix, err := randSuffix()
		if err != nil {
			return nil, err
		}
		id = time.Now().UTC().Format("20060102-150405") + "-" + suffix
		path = filepath.Join(dir, id+".jsonl")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			break
		}
		if tries == 16 {
			return nil, fmt.Errorf("session id: %s is taken and no free suffix was found", id)
		}
	}
	// The file is written with the first event, not here: a start that
	// never gets to a prompt — no key, a bad key, a TUI opened and closed
	// — would otherwise leave an empty session in the list every time.
	return &Session{
		Meta:      Meta{ID: id, Cwd: cwd, Provider: provider, Model: model, Created: time.Now().UTC()},
		path:      path,
		unwritten: true,
	}, nil
}

// randSuffix distinguishes sessions started in the same second. It is the
// only thing that does, and a collision is not cosmetic: the second
// session's first Append rewrites the file at the shared path from
// scratch, destroying the first session's conversation. So a suffix that
// cannot be produced is fatal to New rather than silently 000000, which
// is what a failed open or a short read of /dev/urandom used to give.
func randSuffix() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("session id: %w", err)
	}
	return fmt.Sprintf("%02x%02x%02x", b[0], b[1], b[2]), nil
}

// Open loads an existing session by ID and returns its messages.
func Open(dir, id string) (*Session, []llm.Message, error) {
	if strings.ContainsAny(id, "/\\") || id == "" {
		return nil, nil, fmt.Errorf("invalid session id %q", id)
	}
	s, msgs, _, err := open(filepath.Join(dir, id+".jsonl"), id, nil)
	return s, msgs, err
}

// open reads a session file.
//
// want, when given, is consulted as soon as the metadata line has been
// parsed. Returning false stops the read there and reports matched
// false: a listing then pays one line for a session belonging to some
// other project instead of parsing and folding a whole conversation it
// is going to throw away. That is the whole reason this is not just
// Open — the sessions directory holds every project's sessions, and
// /sessions, --continue and the doctor each walk all of them.
func open(path, id string, want func(Meta) bool) (*Session, []llm.Message, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false, err
	}
	defer f.Close()
	s := &Session{path: path, torn: !endsWithNewline(f)}
	sc := bufio.NewScanner(f)
	// A session line is one JSON event and is usually well under 64 KB;
	// the ceiling is high because a single tool result can be enormous.
	// The scanner doubles its buffer as it needs to, so starting at the
	// ceiling's old 1 MB only meant allocating a megabyte per session
	// opened -- which a listing does once per file in the directory, for
	// every project's sessions, not just this one's.
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var l record
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			// A torn final line (crash mid-write) is dropped, not fatal.
			if sc.Scan() {
				return nil, nil, false, fmt.Errorf("session %s line %d: %w", id, n, err)
			}
			s.torn = true
			break
		}
		switch l.Type {
		case "meta":
			if l.Meta != nil {
				s.Meta = *l.Meta
			}
			if want != nil && !want(s.Meta) {
				return s, nil, false, nil
			}
		case "event":
			e := l.Event
			if e.Seq > s.nextSeq {
				s.nextSeq = e.Seq
			}
			s.ev = append(s.ev, e)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, false, err
	}
	if s.Meta.ID == "" {
		return nil, nil, false, fmt.Errorf("session %s has no metadata", id)
	}
	s.opened = len(s.ev)
	return s, FoldSurface(s.ev), true, nil
}

// endsWithNewline reports whether the log's last byte is a newline, which
// is what tells a complete final record from one a crash cut short. An
// empty or unreadable file counts as complete: there is nothing there for
// the next append to be glued onto.
func endsWithNewline(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return true
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.Size()-1); err != nil {
		return true
	}
	return b[0] == '\n'
}

// SetSettled marks the session done with, or active again.
func (s *Session) SetSettled(v bool) error {
	return s.UpdateMeta(func(m *Meta) { m.Settled = v })
}

// ToggleSettled flips Settled and returns the new value.
func (s *Session) ToggleSettled() (bool, error) {
	var v bool
	err := s.UpdateMeta(func(m *Meta) { v = !m.Settled; m.Settled = v })
	return v, err
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

// EventCount is how many records the log holds. It answers the question
// "has anything been appended since I last looked?" without the copy of
// every event that Events() makes — the terminal's status line asks on
// every frame.
func (s *Session) EventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.ev)
}

// Append writes one event and returns the seq it was given. Seq is assigned
// if zero. Prior events are never rewritten.
func (s *Session) Append(e Event) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unwritten || s.torn {
		// Either there is no file yet, or the one on disk ends in the
		// wreckage of an interrupted write. Both are answered by writing
		// the whole log out from memory before anything is appended.
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
	b, err := json.Marshal(eventLine{Type: "event", Event: e})
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	// The record and its newline go down in one write. Through a
	// bufio.Writer a line longer than its buffer was two — the JSON, then
	// the newline — and another process appending to the same log between
	// them glued its record onto this one. A write that fails part way
	// (a full disk) leaves a partial line on disk exactly as a crash does,
	// so it is marked torn the same way and the next write rewrites the
	// file from memory instead of appending onto the wreckage.
	if _, err := f.Write(append(b, '\n')); err != nil {
		s.torn = true
		f.Close()
		return 0, err
	}
	// The line is in the kernel now, so it survives janetcode crashing,
	// which is the failure a session log is for. Forcing it to the platter
	// as well costs 3.6ms an event on this machine, and a turn writes
	// dozens: the tool call, its arguments, its result, every notice and
	// the verdict. That is paid on the event path, between the model
	// answering and the user seeing it. Sync at most once a second
	// instead, which bounds what a power cut can take to the events of one
	// second; a clean end rewrites and syncs the whole file anyway
	// (SetSettled), and Sync forces it on demand.
	if now := time.Now(); now.Sub(s.lastSync) >= syncInterval {
		if err := f.Sync(); err != nil {
			f.Close()
			return 0, err
		}
		s.lastSync = now
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	s.ev = append(s.ev, e)
	// The event stands whether or not its audit record could be
	// written: VerifyAudit reports the gap.
	sum := sha256.Sum256(b)
	s.lastDigest = hex.EncodeToString(sum[:])
	_ = s.auditLocked()
	return e.Seq, nil
}

// syncInterval bounds how much of the log a power cut can cost.
const syncInterval = time.Second

// Sync forces the log to durable storage. Appending does this on its own
// schedule; call it when the session is about to be left alone.
func (s *Session) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	s.lastSync = time.Now()
	return f.Sync()
}

// SetTitle stores a title in the metadata line and rewrites the file.
func (s *Session) SetTitle(title string) error {
	return s.UpdateMeta(func(m *Meta) { m.Title = strings.TrimSpace(title) })
}

// Title is the session's title, "" when it has none. Read it through
// here, not Meta.Title, from any goroutine but the one that sets it.
func (s *Session) Title() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Meta.Title
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

// CleanTitle drops harness text that got glued onto a person's words.
// A repository map appended on the same line turned "reply with the
// single word ok" into "reply with the single word ok[repository map: 839".
func CleanTitle(s string) string {
	for _, mark := range []string{"[repository map:", "[harness]"} {
		if i := strings.Index(s, mark); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// AutoTitle derives a title from the first user message: its first line,
// trimmed to a few words. It is the fallback when no model wrote one.
// syntheticPrefixes open the user messages janetcode writes on the
// user's behalf: the output of a "!" command shared with the model, a
// goal round, an undo or redo note. They are user messages because that
// is the only role a provider accepts them in, but nobody typed them,
// so naming a session after one gave lists full of rows called "I ran
// this command myself in /tmp/..." and "Work on this goal until it is
// complete or you are blocked".
var syntheticPrefixes = []string{
	"I ran this command myself in ",
	"Work on this goal until it is complete",
	"Continue the goal: ",
	"I undid change #",
	"I reapplied the file changes",
	"I reverted the file changes",
	"The user undid the previous turn",
	"/recipe run ",
}

// HarnessNote reports whether a user message is one of the notes the
// harness writes on the user's behalf (an undo, a redo, a shared "!"
// command, a goal round): text that should read as a note about what
// happened, not as a prompt someone typed. Unlike Synthetic it does not
// treat every bracketed message as machinery, since a person may well
// type "[WIP] keep going".
func HarnessNote(text string) bool {
	for _, p := range syntheticPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// Synthetic reports whether a user message is machinery rather than
// something the person typed.
func Synthetic(text string) bool {
	if strings.HasPrefix(text, "[") || strings.HasPrefix(text, "<") {
		return true
	}
	return HarnessNote(text)
}

func AutoTitle(msgs []llm.Message) string {
	for _, m := range msgs {
		if m.Role != llm.RoleUser {
			continue
		}
		t := CleanTitle(strings.TrimSpace(m.Text()))
		if t == "" || Synthetic(t) {
			continue
		}
		words := strings.Fields(textfmt.FirstLine(t, 200))
		cut := len(words) > 8 || strings.Contains(t, "\n")
		if len(words) > 8 {
			words = words[:8]
		}
		title := strings.TrimRight(strings.Join(words, " "), ".,;:…")
		if cut {
			// A session list that shows "fix the flaky test in the"
			// reads as a typo; the ellipsis says the prompt went on.
			title += "…"
		}
		return title
	}
	return ""
}

// SavePins stores the pins in the metadata line and rewrites the file.
func (s *Session) SavePins(pins []string) error {
	return s.UpdateMeta(func(m *Meta) { m.Pins = append([]string(nil), pins...) })
}

// SetLoops stores armed session loops and rewrites the metadata line.
func (s *Session) SetLoops(loops []LoopSpec) error {
	return s.UpdateMeta(func(m *Meta) { m.Loops = append([]LoopSpec(nil), loops...) })
}

// SetExtraDirs stores additional file-tool roots and rewrites metadata.
func (s *Session) SetExtraDirs(dirs []string) error {
	return s.UpdateMeta(func(m *Meta) { m.ExtraDirs = append([]string(nil), dirs...) })
}

// SetCwd records the directory the session is rooted at and rewrites the
// metadata line, so --resume comes back to where /cd left it rather than
// to the directory the session was opened in.
func (s *Session) SetCwd(cwd string) error {
	return s.UpdateMeta(func(m *Meta) { m.Cwd = cwd })
}

// SetStart records the session's git start state and rewrites metadata.
func (s *Session) SetStart(st StartState) error {
	return s.UpdateMeta(func(m *Meta) { m.Start = &st })
}

// EnsureWritten creates the session file if the first event has not
// landed yet, so a background child can --resume it.
func (s *Session) EnsureWritten() error {
	return s.rewrite()
}

// PIDPath is the sidecar a background child writes its pid into.
func (s *Session) PIDPath() string {
	return strings.TrimSuffix(s.path, ".jsonl") + ".pid"
}

// WritePID records a live background process for this session.
func (s *Session) WritePID(pid int) error {
	if pid <= 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}
	if err := s.EnsureWritten(); err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(s.PIDPath(), []byte(fmt.Sprintf("%d\n", pid)), fsutil.PrivateFileMode)
}

// ClearPID removes the background pid sidecar.
func (s *Session) ClearPID() error {
	err := os.Remove(s.PIDPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// PID returns the recorded background pid, if the sidecar exists.
func (s *Session) PID() (int, bool) {
	pid := readPID(s.PIDPath())
	return pid, pid > 0
}

// UpdateMeta applies fn to the session's metadata and rewrites the file,
// both under the session lock, so a metadata change is never torn by a
// concurrent Append or another update. Every Set* metadata setter is one
// call to it.
func (s *Session) UpdateMeta(fn func(*Meta)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.Meta)
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
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	if err := writeLine(w, line{Type: "meta", Meta: &s.Meta}); err != nil {
		return err
	}
	for i := range s.ev {
		if err := writeLine(w, eventLine{Type: "event", Event: s.ev[i]}); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	// The flags are cleared only once the file is replaced: a rewrite that
	// fails (a full disk, a directory gone read-only) leaves the torn file
	// where it was, and the next Append has to know to rewrite it again
	// rather than append onto its partial last line.
	if err := fsutil.WriteFileAtomic(s.path, buf.Bytes(), fsutil.PrivateFileMode); err != nil {
		return err
	}
	s.unwritten, s.torn = false, false
	return nil
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
