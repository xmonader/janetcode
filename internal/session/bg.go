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
	// Question is the model's open question (the ask tool): the run
	// waits on it, or — Proceeded — went on with the recommendation and
	// still takes a late answer.
	Question *BgQuestion `json:"question,omitempty"`
	// Pending is the one question the run keeps for a person (see
	// BgPending); unlike Ask and Question it outlives the run.
	Pending *BgPending `json:"pending_question,omitempty"`
	// Mandate is the trust level and permission mode the run started
	// under, for the check when it is recovered (see Mandate).
	Mandate *Mandate `json:"mandate,omitempty"`
}

// Mandate is the authority a run was started under, as names: the trust
// level of its project ("untrusted", "undecided", "instructions", "full")
// and its permission mode ("readonly", "default", "auto-edits", "auto",
// "yolo").
type Mandate struct {
	Trust string `json:"trust"`
	Mode  string `json:"mode"`
}

// BgPending is the question a run needs a person for, whichever way it
// asked: the model's ask tool, or a call it could not get approved. There
// is at most one. Asking it again changes nothing; a different one replaces
// it; ordinary progress never clears it, only an answer (ClearPending) or
// the run withdrawing it.
type BgPending struct {
	Text    string    `json:"text"`
	Choices []string  `json:"choices,omitempty"`
	Asked   time.Time `json:"asked"`
	About   string    `json:"about"`         // "question", or the tool of a call awaiting approval
	Ref     string    `json:"ref,omitempty"` // the id of the Question or Ask it came from
}

// SetPending records p as the pending question: the same text keeps its
// original Asked time, a different one replaces the old.
func (st *BgStatus) SetPending(p BgPending) {
	if old := st.Pending; old != nil && old.Text == p.Text {
		p.Asked = old.Asked
	}
	if p.Asked.IsZero() {
		p.Asked = time.Now()
	}
	st.Pending = &p
}

// ClearPending drops the pending question when it came from ref (an empty
// ref clears whatever is pending): an answer to an earlier, replaced
// question must not clear the current one.
func (st *BgStatus) ClearPending(ref string) {
	if st.Pending != nil && (ref == "" || st.Pending.Ref == ref) {
		st.Pending = nil
	}
}

// BgQuestion is a question from the ask tool in a background run. It is
// answered with janetcode --answer <id> <choice> through BgReplyPath.
type BgQuestion struct {
	ID        string           `json:"id"`
	Items     []BgQuestionItem `json:"items"`
	Asked     time.Time        `json:"asked"`
	Deadline  time.Time        `json:"deadline,omitzero"`
	Proceeded bool             `json:"proceeded,omitempty"`
}

// BgQuestionItem is one question of a BgQuestion.
type BgQuestionItem struct {
	Prompt      string   `json:"prompt"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended,omitempty"`
}

// BgReply answers the BgQuestion with the same ID: one choice per item,
// an option label or free text.
type BgReply struct {
	ID      string   `json:"id"`
	Answers []string `json:"answers"`
}

// BgReplyPath is where an answer to a background run's question goes. It
// is apart from BgAnswerPath: a permission request and an open question
// can be pending at once, and each poller removes an answer it does not
// own as stale.
func BgReplyPath(sessionPath string) string { return sidecar(sessionPath, ".bg-reply.json") }

// WriteBgReply answers the open question.
func WriteBgReply(sessionPath string, r BgReply) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(BgReplyPath(sessionPath), b, fsutil.PrivateFileMode)
}

// TakeBgReply returns the answers to question id and removes them, or ok
// false while there are none. A reply to an earlier question is removed.
func TakeBgReply(sessionPath, id string) ([]string, bool) {
	p := BgReplyPath(sessionPath)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var r BgReply
	if json.Unmarshal(b, &r) != nil || r.ID != id {
		_ = os.Remove(p)
		return nil, false
	}
	_ = os.Remove(p)
	return r.Answers, true
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
	pid := st.PID
	if pid <= 0 {
		// The launcher's own record, from before the child started:
		// the child's pid is in the pid sidecar.
		pid = readPID(sidecar(sessionPath, ".pid"))
	}
	if (st.State == BgRunning || st.State == BgWaiting) && pid > 0 && !processAlive(pid) {
		st.PID = pid
		st.State, st.Ask, st.Question = BgFailed, nil, nil
		st.Detail = fmt.Sprintf("the process (pid %d) stopped without finishing", pid)
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
		if q := st.Question; q != nil && len(q.Items) > 0 {
			return "waiting for an answer: " + q.Items[0].Prompt
		}
		return "waiting for an answer"
	case BgRunning:
		if q := st.Question; q != nil && len(q.Items) > 0 && q.Items[0].Recommended == "" {
			return "running for " + since(now, st.Started) + " · question waiting for you: " + q.Items[0].Prompt
		}
		if q := st.Question; q != nil && q.Proceeded && len(q.Items) > 0 {
			return "running for " + since(now, st.Started) + " · went on with " + q.Items[0].Recommended + " for: " + q.Items[0].Prompt + " (a late answer is still taken)"
		}
		return "running for " + since(now, st.Started)
	case BgDone:
		if st.Pending != nil {
			return "finished with a question waiting " + since(now, st.Ended) + " ago: " + st.Pending.Text
		}
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

// readPID reads a pid sidecar, 0 when there is none.
func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(b)), "%d", &pid); err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// StopBg ends the background run of the session file at sessionPath. It
// refuses a run that is not running or waiting, and one that left no pid.
func StopBg(sessionPath string) error {
	st, ok, err := ReadBgStatus(sessionPath)
	switch {
	case err != nil:
		return err
	case !ok:
		return errors.New("not a background run")
	case st.State != BgRunning && st.State != BgWaiting:
		return fmt.Errorf("the run is %s, not running", st.State)
	case st.PID <= 0:
		return errors.New("the run did not say which process it is")
	}
	return stopProcess(st.PID)
}
