package cron

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// Occurrence kinds recorded in the journal.
const (
	KindAdded     = "added"     // job armed (baseline for catch-up)
	KindFired     = "fired"     // timer fired and the prompt was handed over
	KindCatchup   = "catchup"   // one coalesced firing for occurrences missed while down
	KindStale     = "stale"     // downtime exceeded maxCatchup: missed occurrences dropped
	KindSkipped   = "skipped"   // front end could not run it (turn busy); see Cron.Note
	KindDone      = "done"      // front end finished the turn; see Cron.Note
	KindCancelled = "cancelled" // job removed

	KindObserved       = "observed"        // observe job came due: nothing ran, Detail says what would have
	KindMandateChanged = "mandate-changed" // current authority is wider than the job's mandate: skipped
	KindApproved       = "approved"        // a person changed the job's mode or refreshed its mandate
)

const (
	// maxCatchup bounds how far back a missed occurrence is still worth
	// running: past a day the prompt's premise is likely gone.
	maxCatchup = 24 * time.Hour
	// catchupDelay lets the front end finish starting before the catch-up
	// firing lands.
	catchupDelay = 2 * time.Second
	// journalKeep entries survive compaction; the file is compacted when it
	// grows past journalMax.
	journalKeep = 200
	journalMax  = 400
)

// Occurrence is one journal line: what happened to a job and when.
type Occurrence struct {
	Time   time.Time `json:"t"`
	Job    string    `json:"job"`
	Kind   string    `json:"kind"`
	Missed int       `json:"missed,omitempty"` // catchup/stale: occurrences coalesced or dropped
	Detail string    `json:"detail,omitempty"`
}

// Journal is an append-only JSONL record of job occurrences, kept beside
// the session file. It is the source of "when did this job last run", so
// the session's loop specs need no extra fields and old session files stay
// readable. An empty path keeps entries in memory only.
type Journal struct {
	mu      sync.Mutex
	path    string
	entries []Occurrence
	lines   int
}

// OpenJournal loads path (missing is fine). Malformed lines are skipped:
// the journal is advisory and must never block a resume.
func OpenJournal(path string) *Journal {
	j := &Journal{path: path}
	if path == "" {
		return j
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return j
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		j.lines++
		var o Occurrence
		if json.Unmarshal(sc.Bytes(), &o) != nil || o.Job == "" || o.Kind == "" {
			continue
		}
		j.entries = append(j.entries, o)
	}
	return j
}

// Append records o (Time defaults to now) and persists it best-effort.
func (j *Journal) Append(o Occurrence) {
	if j == nil {
		return
	}
	if o.Time.IsZero() {
		o.Time = time.Now()
	}
	o.Detail = strings.TrimSpace(o.Detail)
	if len(o.Detail) > 200 {
		o.Detail = o.Detail[:200]
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, o)
	if len(j.entries) > journalMax {
		j.entries = append([]Occurrence(nil), j.entries[len(j.entries)-journalKeep:]...)
	}
	if j.path == "" {
		return
	}
	j.lines++
	if j.lines > journalMax {
		var buf bytes.Buffer
		for _, e := range j.entries {
			b, _ := json.Marshal(e)
			buf.Write(b)
			buf.WriteByte('\n')
		}
		if fsutil.AtomicWriteMode(j.path, buf.Bytes(), fsutil.PrivateFileMode) == nil {
			j.lines = len(j.entries)
		}
		return
	}
	b, _ := json.Marshal(o)
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, fsutil.PrivateFileMode)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

// Recent returns up to n newest entries, oldest first. job filters when set.
func (j *Journal) Recent(n int, job string) []Occurrence {
	if j == nil || n <= 0 {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Occurrence
	for i := len(j.entries) - 1; i >= 0 && len(out) < n; i-- {
		if job == "" || j.entries[i].Job == job {
			out = append(out, j.entries[i])
		}
	}
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}

// LastRun is the newest time job was armed or ran (fired or caught up), the
// baseline from which missed occurrences are counted.
func (j *Journal) LastRun(job string) (time.Time, bool) {
	if j == nil {
		return time.Time{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for i := len(j.entries) - 1; i >= 0; i-- {
		e := j.entries[i]
		if e.Job != job {
			continue
		}
		switch e.Kind {
		case KindAdded, KindFired, KindCatchup, KindStale, KindObserved, KindMandateChanged:
			return e.Time, true
		case KindCancelled:
			return time.Time{}, false
		}
	}
	return time.Time{}, false
}

// Missed applies the catch-up policy: occurrences that fell due between
// last and now while the process was down coalesce into ONE firing, never
// a burst. Downtime beyond maxCatchup is dropped (stale). It returns the
// number of missed occurrences and whether to run a catch-up.
func Missed(last, now time.Time, every time.Duration) (missed int, run bool) {
	if every <= 0 || last.IsZero() || !now.After(last) {
		return 0, false
	}
	gap := now.Sub(last)
	missed = int(gap / every)
	if missed == 0 {
		return 0, false
	}
	return missed, gap <= maxCatchup
}
