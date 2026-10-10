package agent

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/session"
)

// TestReadBlamePrescreen is the offline pre-screen for blame notes on
// reads: over recorded sessions (a session's .jsonl beside its .undo.json,
// which holds every change with its content and intent), how many
// successful read_file results show at least readBlameMinLines lines an
// earlier turn wrote with a stated intent — the reads that would have
// carried a note. It also counts the looser rule (an earlier call of the
// same turn), which the feature does not use, to show what that would
// add. Skipped unless JANETCODE_READBLAME_PRESCREEN names a directory to
// walk:
//
//	JANETCODE_READBLAME_PRESCREEN=docs/benchmarks/effbench/runs go test ./internal/agent -run ReadBlamePrescreen -v
func TestReadBlamePrescreen(t *testing.T) {
	root := os.Getenv("JANETCODE_READBLAME_PRESCREEN")
	if root == "" {
		t.Skip("JANETCODE_READBLAME_PRESCREEN not set")
	}
	var sessions, multiTurn, reads, readsAfterChange, hitEarlierTurn, hitSameTurn, notes int
	var noteBytes int
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".undo.json") {
			return nil
		}
		dir, id := filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".undo.json")
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		var undo struct {
			Changes []change.Change `json:"changes"`
		}
		if json.Unmarshal(raw, &undo) != nil {
			return nil
		}
		sess, _, err := session.Open(dir, id)
		if err != nil {
			return nil
		}
		sessions++
		events := sess.Events()
		resultSeq := map[string]int{}
		maxTurn := 0
		for _, e := range events {
			if e.Type == session.TypeToolResult && e.CallID != "" {
				resultSeq[e.CallID] = e.Seq
			}
			maxTurn = max(maxTurn, e.Turn)
		}
		if maxTurn > 1 {
			multiTurn++
		}
		for _, e := range events {
			if e.Type != session.TypeToolResult || e.Name != "read_file" || e.IsError {
				continue
			}
			reads++
			var in struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(e.Arguments), &in) != nil || in.Path == "" {
				continue
			}
			path := in.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(sess.Meta.Cwd, path)
			}
			var before []change.Change
			for _, c := range undo.Changes {
				if s, ok := resultSeq[c.Call]; c.Path == path && (ok && s < e.Seq || c.Tool == "undo") {
					before = append(before, c)
				}
			}
			if len(before) == 0 {
				continue
			}
			readsAfterChange++
			owners := readBlamePrescreenOwners(before)
			bySeq := map[int]Change{}
			for _, c := range before {
				bySeq[c.Seq] = c
			}
			lo, hi := readBlameShown(e.Output)
			if lo == 0 {
				continue
			}
			if spans := readBlameSessionSpans(owners, bySeq, e.Turn, lo, hi, nil); len(spans) > 0 {
				hitEarlierTurn++
				notes++
				noteBytes += len(readBlameFormat(spans))
			}
			if spans := readBlameSessionSpans(owners, bySeq, e.Turn+1, lo, hi, nil); len(spans) > 0 {
				hitSameTurn++
			}
		}
		return nil
	})
	t.Logf("%d sessions with an undo record (%d with more than one turn); %d successful reads, %d of a file the session had already changed", sessions, multiTurn, reads, readsAfterChange)
	t.Logf("reads that would carry a note (an earlier turn wrote >=%d shown lines with an intent): %d (%.1f%% of reads)", readBlameMinLines, hitEarlierTurn, readBlamePct(hitEarlierTurn, reads))
	t.Logf("looser rule, any earlier call including the same turn: %d (%.1f%% of reads)", hitSameTurn, readBlamePct(hitSameTurn, reads))
	if notes > 0 {
		t.Logf("mean note size %d bytes", noteBytes/notes)
	}
}

func readBlamePct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

// readBlamePrescreenOwners replays changes (oldest first) the way Blame
// does and returns the change number each final line comes from.
func readBlamePrescreenOwners(changes []change.Change) []int {
	cur := diff.SplitLinesKeepEOL(string(changes[0].Before))
	owners := make([]int, len(cur))
	for _, c := range changes {
		next := diff.SplitLinesKeepEOL(string(c.After))
		var nextOwners []int
		oi := 0
		for _, e := range change.DiffLines(cur, next) {
			switch e.Op {
			case '=':
				nextOwners = append(nextOwners, owners[oi])
				oi++
			case '-':
				oi++
			case '+':
				nextOwners = append(nextOwners, c.Seq)
			}
		}
		cur, owners = next, nextOwners
	}
	return owners
}
