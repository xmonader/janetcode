package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
)

// TestEvidencePrescreen is the offline, zero-token pre-screen for signed
// evidence: over recorded sessions, how much of what each code-changing
// turn claimed would land in the evidence as a re-executable record
// (exit code and a comparable output hash), how much only as an exit
// code, and how much as an unverified assertion. It re-runs nothing.
// Skipped unless JANETCODE_EVIDENCE_PRESCREEN names a sessions directory:
//
//	JANETCODE_EVIDENCE_PRESCREEN=~/.local/share/janetcode/sessions go test ./internal/agent -run EvidencePrescreen -v
func TestEvidencePrescreen(t *testing.T) {
	dir := os.Getenv("JANETCODE_EVIDENCE_PRESCREEN")
	if dir == "" {
		t.Skip("JANETCODE_EVIDENCE_PRESCREEN not set")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var sessions, turns, cmds, comparable, exitOnly, withheld, stale, unverified, bare int
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		sess, _, err := session.Open(dir, id)
		if err != nil {
			continue
		}
		events := sess.Events()
		root := sess.Meta.Cwd
		// A code-changing turn is one the verdict fold counts edits in:
		// sessions recorded before file/change events existed have only
		// the edit tools' results to say so.
		var turnNums []int
		seen := map[int]bool{}
		for _, e := range events {
			if e.Turn > 0 && !seen[e.Turn] {
				seen[e.Turn] = true
				turnNums = append(turnNums, e.Turn)
			}
		}
		counted := false
		for _, tn := range turnNums {
			o := FoldOutcomeIn(events, tn, root)
			if o.Edited == 0 {
				continue
			}
			if !counted {
				sessions++
				counted = true
			}
			turns++
			rec := proof.Record{
				Files: []proof.File{{Path: "-", Changes: []proof.ChangeRef{{Turn: tn}}}},
				Turns: []proof.Turn{{Turn: tn}},
			}
			se := proof.SessionFromEvents(rec, proof.EvidenceInput{
				RepoRoot: root, WorkRoot: root, Events: events, Claims: map[int]string{tn: o.Claimed},
			})
			te := se.Turns[0]
			if len(te.Commands) == 0 {
				if te.Claimed != "" {
					unverified++
				} else {
					bare++
				}
			}
			for _, c := range te.Commands {
				cmds++
				switch {
				case c.Withheld:
					withheld++
				case c.Output != "" && !c.OutputPartial && c.Exit != proof.ExitUnknownFailure:
					comparable++
				default:
					exitOnly++
				}
				if c.Stale {
					stale++
				}
			}
		}
	}
	claims := cmds + unverified
	share := func(n int) float64 {
		if claims == 0 {
			return 0
		}
		return 100 * float64(n) / float64(claims)
	}
	t.Logf("sessions with code changes %d · code-changing turns %d (%d with no check and no claim)", sessions, turns, bare)
	t.Logf("claims %d: re-executable with comparable output %d (%.1f%%) · exit code only %d (%.1f%%) · withheld %d · unverified assertions %d (%.1f%%)",
		claims, comparable, share(comparable), exitOnly, share(exitOnly), withheld, unverified, share(unverified))
	t.Logf("recorded commands that ran before a later edit in their turn (stale): %d of %d", stale, cmds)
}
