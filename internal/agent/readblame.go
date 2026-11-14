package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Agent blame on reads (ReadBlame): when read_file shows lines an earlier
// agent turn wrote for a stated reason, the result carries one line per
// range saying so — "L40–52: turn 3 (#7): fix the off-by-one in paging ·
// checked by go test ./..." — so a later turn sees a deliberate fix as one
// before it "tidies" it away. Re-breaking an earlier fix is a common way an
// agent undoes its own work, and the reason was already recorded: the
// session's provenance (Change.Intent) for lines written in this session,
// and the proof notes (internal/proof, refs/notes/janetcode) for lines a
// committed session wrote.
//
// Only agent-written lines with an intent are noted, never the current
// turn's own (the model has just written them), never what was undone,
// and never a file edited outside the session since the last recorded
// change (the attribution would be of a state the file is no longer in).
// The note is bounded by readBlameBudget bytes and readBlameMaxSpans
// ranges a read.
//
// It is opt-in (config read_blame): it changes what the model reads, and
// nothing on effbench has measured it yet. JANETCODE_OFF=readblame turns
// it off again.

const (
	readBlameBudget   = 480
	readBlameMaxSpans = 4
	// readBlameMinLines leaves out a range shorter than this: a one-line
	// rename is not a decision worth a line of context.
	readBlameMinLines = 2
	readBlameTimeout  = 3 * time.Second
)

// SetReadBlame turns blame notes on reads on or off.
func (a *Agent) SetReadBlame(on bool) {
	a.featMu.Lock()
	a.ReadBlame = on
	a.featMu.Unlock()
}

// ReadBlameOn is ReadBlame, safe to read while a turn runs, and off when
// JANETCODE_OFF names readblame.
func (a *Agent) ReadBlameOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.ReadBlame && !a.Off["readblame"]
}

// readBlameSpan is one range of shown lines written for one reason.
type readBlameSpan struct {
	from, to int // 1-based, inclusive
	label    string
}

// readBlameState caches the committed attribution of a file version, so a
// second read of the same file costs no git blame. read_file runs in
// parallel goroutines, hence the lock.
type readBlameState struct {
	mu        sync.Mutex
	committed map[string][]proof.Line // by path + content digest
	noNotes   bool                    // the repository carries no proof notes
}

// readBlame is the note appended to a read_file result, or "".
func (a *Agent) readBlame(ctx context.Context, name string, input json.RawMessage, out string, isErr bool) string {
	if name != "read_file" || isErr || !a.ReadBlameOn() {
		return ""
	}
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || strings.TrimSpace(in.Path) == "" {
		return ""
	}
	lo, hi := readBlameShown(out)
	if lo == 0 {
		return ""
	}
	path := in.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.displayRoot(), path)
	}
	spans := a.readBlameSession(path, lo, hi)
	if len(spans) == 0 {
		spans = a.readBlameCommitted(ctx, path, lo, hi)
	}
	return readBlameFormat(spans)
}

// readBlameLineRe is a read_file line's number: "12\t" or, with anchors,
// "12:abc\t".
var readBlameLineRe = regexp.MustCompile(`^(\d+)(?::[a-z]+)?\t`)

// readBlameShown is the first and last line number a read_file result
// shows, or 0, 0 when it shows none (a notebook, an extracted PDF, an
// offset past the end).
func readBlameShown(out string) (lo, hi int) {
	for _, line := range strings.Split(out, "\n") {
		m := readBlameLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if lo == 0 {
			lo = n
		}
		hi = n
	}
	return lo, hi
}

// readBlameSession attributes the shown lines from this session's own
// changes.
func (a *Agent) readBlameSession(path string, lo, hi int) []readBlameSpan {
	lines, note, err := a.Blame(path)
	if err != nil || note != "" {
		return nil
	}
	bySeq := map[int]Change{}
	for _, c := range a.Changes() {
		if c.Path == path {
			bySeq[c.Seq] = c
		}
	}
	owners := make([]int, len(lines))
	for i, l := range lines {
		owners[i] = l.Seq
	}
	checks := a.readBlameChecks()
	return readBlameSessionSpans(owners, bySeq, a.turnN, lo, hi, checks)
}

// readBlameSessionSpans groups the shown lines lo..hi by the change that
// wrote them, keeping the ranges an earlier turn wrote with an intent.
// owners[i] is the change number line i+1 comes from, 0 for none; checks
// is the last passing check of each turn, by turn.
func readBlameSessionSpans(owners []int, bySeq map[int]Change, turn, lo, hi int, checks map[int]string) []readBlameSpan {
	var out []readBlameSpan
	hi = min(hi, len(owners))
	for i := lo; i <= hi; {
		seq := owners[i-1]
		j := i
		for j+1 <= hi && owners[j] == seq {
			j++
		}
		c, ok := bySeq[seq]
		if ok && seq > 0 && c.Turn < turn && c.Tool != "undo" && !c.Undone && strings.TrimSpace(c.Intent) != "" && j-i+1 >= readBlameMinLines {
			label := fmt.Sprintf("turn %d (#%d", c.Turn, c.Seq)
			if !c.When.IsZero() {
				label += ", " + c.When.Format("2006-01-02")
			}
			label += "): " + c.Intent
			if chk := checks[c.Turn]; chk != "" {
				label += " · checked by " + textfmt.Clip(chk, 60)
			}
			out = append(out, readBlameSpan{from: i, to: j, label: label})
		}
		i = j + 1
	}
	return out
}

// readBlameChecks is the last check each earlier turn ran that passed with
// a passing verdict, from the verdicts in the session log.
func (a *Agent) readBlameChecks() map[int]string {
	out := map[int]string{}
	if a.Log == nil {
		return out
	}
	for _, e := range a.logEvents() {
		if e.Type != session.TypeVerdict || e.IsError || len(e.Extra) == 0 {
			continue
		}
		var o TurnOutcome
		if json.Unmarshal(e.Extra, &o) != nil || len(o.Checks) == 0 || o.Failed > 0 {
			continue
		}
		out[e.Turn] = o.Checks[len(o.Checks)-1]
	}
	return out
}

// readBlameCommitted attributes the shown lines from committed history:
// git blame, and the proof note of the commit that wrote each line.
func (a *Agent) readBlameCommitted(ctx context.Context, path string, lo, hi int) []readBlameSpan {
	repo := a.userRepo()
	if repo == nil {
		return nil
	}
	st := &a.readBlameCache
	st.mu.Lock()
	if st.noNotes {
		st.mu.Unlock()
		return nil
	}
	st.mu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readBlameTimeout)
	defer cancel()
	if _, err := gitx.Run(ctx, repo.Root, nil, "rev-parse", "--verify", "-q", proof.NotesRef); err != nil {
		st.mu.Lock()
		st.noNotes = true
		st.mu.Unlock()
		return nil
	}
	key := fmt.Sprintf("%s\x00%x", path, sha256.Sum256(data))
	st.mu.Lock()
	lines, ok := st.committed[key]
	st.mu.Unlock()
	if !ok {
		lines, err = proof.BlameCommitted(ctx, repo.Root, path)
		if err != nil {
			return nil
		}
		st.mu.Lock()
		if st.committed == nil {
			st.committed = map[string][]proof.Line{}
		}
		st.committed[key] = lines
		st.mu.Unlock()
	}
	return readBlameCommittedSpans(lines, lo, hi)
}

// readBlameCommittedSpans groups shown committed lines by commit, keeping
// the ranges whose commit's proof note gave an intent.
func readBlameCommittedSpans(lines []proof.Line, lo, hi int) []readBlameSpan {
	var out []readBlameSpan
	at := func(n int) (proof.Line, bool) {
		if n < 1 || n > len(lines) {
			return proof.Line{}, false
		}
		return lines[n-1], true
	}
	for i := lo; i <= hi; {
		l, ok := at(i)
		if !ok {
			break
		}
		j := i
		for {
			next, ok := at(j + 1)
			if !ok || j+1 > hi || next.Commit != l.Commit || next.Intent != l.Intent {
				break
			}
			j++
		}
		if l.Intent != "" && !readBlameUncommitted(l.Commit) && j-i+1 >= readBlameMinLines {
			label := "commit " + proof.Short(l.Commit)
			if l.Verdict != "" {
				label += " (" + l.Verdict + ")"
			}
			out = append(out, readBlameSpan{from: i, to: j, label: label + ": " + l.Intent})
		}
		i = j + 1
	}
	return out
}

// readBlameUncommitted reports git blame's all-zero commit, its name for
// lines not committed yet.
func readBlameUncommitted(sha string) bool { return strings.Trim(sha, "0") == "" }

// readBlameFormat renders spans as the note, longest range first when
// the budget cannot hold them all, and in file order as shown.
func readBlameFormat(spans []readBlameSpan) string {
	if len(spans) == 0 {
		return ""
	}
	if len(spans) > readBlameMaxSpans {
		// Keep the widest ranges: they are the decisions most of what was
		// shown depends on.
		keep := append([]readBlameSpan(nil), spans...)
		for i := 1; i < len(keep); i++ {
			for j := i; j > 0 && keep[j].to-keep[j].from > keep[j-1].to-keep[j-1].from; j-- {
				keep[j], keep[j-1] = keep[j-1], keep[j]
			}
		}
		keep = keep[:readBlameMaxSpans]
		var ordered []readBlameSpan
		for _, s := range spans {
			for _, k := range keep {
				if s == k {
					ordered = append(ordered, s)
					break
				}
			}
		}
		spans = ordered
	}
	var b strings.Builder
	b.WriteString("\n[blame: lines an earlier agent turn wrote on purpose]")
	for _, s := range spans {
		line := fmt.Sprintf("\nL%d–%d %s", s.from, s.to, s.label)
		if b.Len()+len(line) > readBlameBudget {
			line = textfmt.ClipBytes(line, readBlameBudget-b.Len())
			if len(line) < 24 {
				break
			}
			b.WriteString(line)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}
