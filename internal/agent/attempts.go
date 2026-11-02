package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// The subagent attempt store: a durable record of every attempt a subagent
// makes, written the way a write-ahead log is. An *intent* line lands
// before the child starts and a *completion* line after it ends, so a
// parent killed mid-run finds, on resume, the attempts that have an intent
// and no completion. Those are reported and closed as orphaned; they are
// never rerun, because the child may have edited files the parent cannot
// see (a fork writes the live tree, a merge-back lands in it). The files an
// attempt changed carry its id (Change.Attempt), which is what lets
// /undo agent <id> unwind exactly that child's work. Modelled on grok's
// attempt_store intent/completion/recovery/rewind records, kept to what
// this agent's session model needs.

// Attempt outcomes. An attempt with no completion line is open.
const (
	OutcomeOK        = "ok"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
	OutcomeTimeout   = "timeout"
	// OutcomeOrphaned is written by recovery for an attempt whose parent
	// died before it could record how it ended.
	OutcomeOrphaned = "orphaned"
)

const maxAttemptLines = 500

// Attempt is one subagent run as the store folds its records.
type Attempt struct {
	ID       string    `json:"id"`
	Child    string    `json:"child"`
	Desc     string    `json:"desc,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Edit     bool      `json:"edit,omitempty"`
	Fork     bool      `json:"fork,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Turn     int       `json:"turn,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitempty"`
	Outcome  string    `json:"outcome,omitempty"` // "" while open
	Detail   string    `json:"detail,omitempty"`
}

// Open reports an attempt with an intent and no completion.
func (a Attempt) Open() bool { return a.Outcome == "" }

type attemptLine struct {
	Rec string `json:"rec"` // "intent" or "completion"
	Attempt
}

// AttemptStore is the JSONL file of attempt records. A nil store records
// nothing, so a session without one behaves as before.
type AttemptStore struct {
	mu    sync.Mutex
	path  string
	byID  map[string]*Attempt
	order []string
	lines int
}

// OpenAttempts loads path ("" keeps the store in memory). Lines that do
// not parse, or that complete an attempt never begun, are skipped: the
// store is a record to report from and must never fail a resume.
func OpenAttempts(path string) *AttemptStore {
	s := &AttemptStore{path: path, byID: map[string]*Attempt{}}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		s.lines++
		var l attemptLine
		if json.Unmarshal(sc.Bytes(), &l) != nil || l.ID == "" {
			continue
		}
		s.fold(l)
	}
	return s
}

func (s *AttemptStore) fold(l attemptLine) {
	switch l.Rec {
	case "intent":
		if s.byID[l.ID] != nil {
			return
		}
		a := l.Attempt
		a.Outcome, a.Ended = "", time.Time{}
		s.byID[a.ID] = &a
		s.order = append(s.order, a.ID)
	case "completion":
		a := s.byID[l.ID]
		if a == nil || !a.Open() || l.Outcome == "" {
			return
		}
		a.Outcome, a.Ended, a.Detail = l.Outcome, l.Ended, l.Detail
	}
}

func (s *AttemptStore) write(l attemptLine) {
	s.fold(l)
	if s.path == "" {
		return
	}
	s.lines++
	if s.lines > maxAttemptLines {
		s.compact()
		return
	}
	b, _ := json.Marshal(l)
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fsutil.PrivateFileMode)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

// compact rewrites the file as the newest settled attempts plus every open
// one, so an old file does not grow without bound but nothing unfinished
// is forgotten.
func (s *AttemptStore) compact() {
	settled := 0
	for _, id := range s.order {
		if !s.byID[id].Open() {
			settled++
		}
	}
	drop := settled - 100
	var buf bytes.Buffer
	var order []string
	for _, id := range s.order {
		a := s.byID[id]
		if !a.Open() && drop > 0 {
			drop--
			delete(s.byID, id)
			continue
		}
		order = append(order, id)
		in := *a
		in.Outcome, in.Ended, in.Detail = "", time.Time{}, ""
		b, _ := json.Marshal(attemptLine{Rec: "intent", Attempt: in})
		buf.Write(append(b, '\n'))
		if !a.Open() {
			b, _ = json.Marshal(attemptLine{Rec: "completion", Attempt: *a})
			buf.Write(append(b, '\n'))
		}
	}
	if fsutil.AtomicWriteMode(s.path, buf.Bytes(), fsutil.PrivateFileMode) == nil {
		s.order = order
		s.lines = strings.Count(buf.String(), "\n")
		return
	}
}

// Begin writes the intent for a new attempt and returns its id ("a1", "a2",
// ... across the session's whole life, so ids stay unique after a resume).
func (s *AttemptStore) Begin(a Attempt) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.order) + 1
	for s.byID[fmt.Sprintf("a%d", n)] != nil {
		n++
	}
	a.ID = fmt.Sprintf("a%d", n)
	if a.Started.IsZero() {
		a.Started = time.Now()
	}
	a.Desc = clipAttempt(a.Desc, 120)
	s.write(attemptLine{Rec: "intent", Attempt: a})
	return a.ID
}

// Finish writes the completion for id. A second completion, or one for an
// unknown id, is ignored.
func (s *AttemptStore) Finish(id, outcome, detail string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.write(attemptLine{Rec: "completion", Attempt: Attempt{ID: id, Outcome: outcome, Ended: time.Now(), Detail: clipAttempt(detail, 200)}})
}

// OpenFor is the id of child's open attempt, or "".
func (s *AttemptStore) OpenFor(child string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		if a := s.byID[s.order[i]]; a.Child == child && a.Open() {
			return a.ID
		}
	}
	return ""
}

// All lists attempts oldest first.
func (s *AttemptStore) All() []Attempt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Attempt, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, *s.byID[id])
	}
	return out
}

// Find resolves ref: an attempt id ("a3") or a child id ("task-2", its
// newest attempt).
func (s *AttemptStore) Find(ref string) (Attempt, bool) {
	ref = strings.TrimSpace(ref)
	all := s.All()
	for _, a := range all {
		if a.ID == ref {
			return a, true
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Child == ref {
			return all[i], true
		}
	}
	return Attempt{}, false
}

// Reconcile is crash recovery: every attempt still open belongs to a parent
// that died, since no other process shares this session. Each is closed as
// orphaned (so it is reported once) and returned. Nothing is rerun.
func (s *AttemptStore) Reconcile() []Attempt {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Attempt
	for _, id := range s.order {
		a := s.byID[id]
		if !a.Open() {
			continue
		}
		out = append(out, *a)
	}
	for _, a := range out {
		s.write(attemptLine{Rec: "completion", Attempt: Attempt{ID: a.ID, Outcome: OutcomeOrphaned, Ended: time.Now(), Detail: "parent exited before the attempt finished"}})
	}
	return out
}

func clipAttempt(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// OrphanNotice is the text shown (and given to the model) for attempts
// recovery found open. changes says how many recorded file changes each
// carries. It tells the model not to rerun them.
func OrphanNotice(orphans []Attempt, changes func(id string) int) string {
	if len(orphans) == 0 {
		return ""
	}
	sort.SliceStable(orphans, func(i, j int) bool { return orphans[i].Started.Before(orphans[j].Started) })
	var b strings.Builder
	fmt.Fprintf(&b, "%d subagent attempt(s) were running when the previous process exited. They are NOT rerun: check the files before repeating their work.", len(orphans))
	for _, a := range orphans {
		fmt.Fprintf(&b, "\n- %s (%s) %q", a.ID, a.Child, a.Desc)
		if n := changes(a.ID); n > 0 {
			fmt.Fprintf(&b, ": %d recorded file change(s), unwind with /undo agent %s", n, a.ID)
		}
		if a.Worktree != "" {
			if _, err := os.Stat(a.Worktree); err == nil {
				fmt.Fprintf(&b, ": its checkout %s may hold unmerged work", a.Worktree)
			}
		}
	}
	return b.String()
}

// ReconcileAttempts is run once when a session is opened: attempts the
// previous process left open are closed as orphaned and remembered for
// InterruptedNotice and ContinueInterrupted. Nothing is rerun.
func (a *Agent) ReconcileAttempts() []Attempt {
	a.orphans = a.Attempts.Reconcile()
	return a.orphans
}

func (a *Agent) attemptChanges(id string) int {
	n := 0
	for _, c := range a.Changes() {
		if c.Attempt == id && !c.Undone && c.Tool != "undo" {
			n++
		}
	}
	return n
}

// OrphanNotice describes the attempts ReconcileAttempts found, or "".
func (a *Agent) OrphanNotice() string { return OrphanNotice(a.orphans, a.attemptChanges) }

// AttemptsReport lists the session's subagent attempts for the user.
func (a *Agent) AttemptsReport() string {
	all := a.Attempts.All()
	if len(all) == 0 {
		return "no subagent attempts recorded"
	}
	var b strings.Builder
	for _, at := range all {
		out := at.Outcome
		if out == "" {
			out = "running"
		}
		fmt.Fprintf(&b, "%s %s %s", at.ID, at.Child, out)
		if n := a.attemptChanges(at.ID); n > 0 {
			fmt.Fprintf(&b, " (%d change(s))", n)
		}
		if at.Desc != "" {
			fmt.Fprintf(&b, ": %s", at.Desc)
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// RewindAttempt unwinds the file changes one subagent attempt made (ref is
// an attempt id or a child id), newest first so later hunks come off before
// earlier ones. A change that no longer applies is reported and skipped;
// the rest still go. It returns one line per change.
func (a *Agent) RewindAttempt(ref string) (string, error) {
	at, ok := a.Attempts.Find(ref)
	if !ok {
		return "", fmt.Errorf("no subagent attempt %q (see /undo agent)", ref)
	}
	var seqs []int
	for _, c := range a.Changes() {
		if c.Attempt == at.ID && !c.Undone && c.Tool != "undo" {
			seqs = append(seqs, c.Seq)
		}
	}
	if len(seqs) == 0 {
		return fmt.Sprintf("%s (%s) has no changes left to undo", at.ID, at.Child), nil
	}
	sort.Sort(sort.Reverse(sort.IntSlice(seqs)))
	var b strings.Builder
	failed := 0
	for _, seq := range seqs {
		msg, err := a.UndoChange(seq)
		if err != nil {
			failed++
			fmt.Fprintf(&b, "#%d: %v\n", seq, err)
			continue
		}
		fmt.Fprintf(&b, "#%d: %s\n", seq, msg)
	}
	fmt.Fprintf(&b, "%s (%s): %d of %d change(s) undone", at.ID, at.Child, len(seqs)-failed, len(seqs))
	return b.String(), nil
}
