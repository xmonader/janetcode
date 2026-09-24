package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// A background run (--bg) is a process nobody is watching. It says how it
// is doing in a sidecar beside its session file, so --sessions can tell a
// run that finished from one that failed or one waiting for an answer,
// and a permission prompt it cannot show on a terminal waits there until
// janetcode --answer settles it.

// Background run states.
const (
	BgRunning = "running"
	BgWaiting = "waiting"
	BgDone    = "done"
	BgFailed  = "failed"
)

// BgStatus is what a background run last said about itself.
type BgStatus struct {
	State   string    `json:"state"`
	PID     int       `json:"pid,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
	// Detail is the first line of the answer when done, the error when
	// failed.
	Detail string `json:"detail,omitempty"`
	// Ask is the permission request the run is waiting on, when waiting.
	Ask *BgAsk `json:"ask,omitempty"`
}

// BgAsk is one permission request from a background run.
type BgAsk struct {
	ID      string    `json:"id"`
	Tool    string    `json:"tool"`
	Summary string    `json:"summary"`
	Warning string    `json:"warning,omitempty"`
	Preview string    `json:"preview,omitempty"`
	Asked   time.Time `json:"asked"`
}

// BgAnswer settles the BgAsk with the same ID.
type BgAnswer struct {
	ID       string `json:"id"`
	Decision string `json:"decision"` // allow, session or deny
}

// BgDecisions are the answers --answer accepts.
var BgDecisions = []string{"allow", "session", "deny"}

func sidecar(sessionPath, suffix string) string {
	return strings.TrimSuffix(sessionPath, ".jsonl") + suffix
}

// BgStatusPath is the status sidecar of the session file at sessionPath.
func BgStatusPath(sessionPath string) string { return sidecar(sessionPath, ".bg.json") }

// BgAnswerPath is where an answer to a waiting background run goes.
func BgAnswerPath(sessionPath string) string { return sidecar(sessionPath, ".bg-answer.json") }

// WriteBgStatus replaces the status sidecar.
func WriteBgStatus(sessionPath string, st BgStatus) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(BgStatusPath(sessionPath), b, fsutil.PrivateFileMode)
}

// ReadBgStatus reads the status sidecar; ok is false for a session that
// never ran in the background. A run whose process is gone without saying
// how it ended is reported as failed.
func ReadBgStatus(sessionPath string) (st BgStatus, ok bool, err error) {
	b, err := os.ReadFile(BgStatusPath(sessionPath))
	if errors.Is(err, fs.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, false, fmt.Errorf("%s: %w", BgStatusPath(sessionPath), err)
	}
	if (st.State == BgRunning || st.State == BgWaiting) && st.PID > 0 && !processAlive(st.PID) {
		st.State, st.Ask = BgFailed, nil
		st.Detail = fmt.Sprintf("the process (pid %d) stopped without finishing", st.PID)
	}
	return st, true, nil
}

// WriteBgAnswer answers the waiting request id.
func WriteBgAnswer(sessionPath string, a BgAnswer) error {
	b, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(BgAnswerPath(sessionPath), b, fsutil.PrivateFileMode)
}

// TakeBgAnswer returns the answer to request id and removes it, or ok
// false while there is none. An answer to an earlier request is stale and
// is removed too.
func TakeBgAnswer(sessionPath, id string) (decision string, ok bool) {
	p := BgAnswerPath(sessionPath)
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var a BgAnswer
	if json.Unmarshal(b, &a) != nil || a.ID != id {
		_ = os.Remove(p)
		return "", false
	}
	_ = os.Remove(p)
	return a.Decision, true
}

// Line is the status as one phrase for a listing, e.g. "waiting for an
// answer: bash go test" or "done 4m ago".
func (st BgStatus) Line(now time.Time) string {
	switch st.State {
	case BgWaiting:
		if st.Ask != nil {
			return "waiting for an answer: " + st.Ask.Tool + " " + st.Ask.Summary
		}
		return "waiting for an answer"
	case BgRunning:
		return "running for " + since(now, st.Started)
	case BgDone:
		if st.Detail != "" {
			return "done " + since(now, st.Ended) + " ago: " + st.Detail
		}
		return "done " + since(now, st.Ended) + " ago"
	case BgFailed:
		if st.Detail != "" {
			return "failed: " + st.Detail
		}
		return "failed"
	}
	return st.State
}

func since(now, t time.Time) string {
	if t.IsZero() {
		return "a while"
	}
	d := now.Sub(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// BgSessionPath is the file of session id in dir, for commands that act
// on a background run by its id.
func BgSessionPath(dir, id string) (string, error) {
	if strings.ContainsAny(id, "/\\") || id == "" {
		return "", fmt.Errorf("invalid session id %q", id)
	}
	return filepath.Join(dir, id+".jsonl"), nil
}
