package agent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/numclaim"
	"github.com/xmonader/janetcode/internal/session"
)

// TestNumclaimPrescreen is the offline pre-screen for checks on numeric
// claims: over recorded sessions, the numbers each turn's final answer
// states and what share of them no measurement in the session backs. It
// re-runs nothing. Skipped unless JANETCODE_NUMCLAIM_PRESCREEN names one
// or more sessions directories (a path list):
//
//	JANETCODE_NUMCLAIM_PRESCREEN=/copy/of/sessions go test ./internal/agent -run NumclaimPrescreen -v
func TestNumclaimPrescreen(t *testing.T) {
	dirs := os.Getenv("JANETCODE_NUMCLAIM_PRESCREEN")
	if dirs == "" {
		t.Skip("JANETCODE_NUMCLAIM_PRESCREEN not set")
	}
	var sessions, answers, withClaims, claims, unbacked int
	byStatus := map[numclaim.Status]int{}
	var samples []string
	for _, dir := range filepath.SplitList(dirs) {
		paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		for _, p := range paths {
			sess, _, err := session.Open(dir, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
			if err != nil {
				continue
			}
			sessions++
			events := sess.Events()
			// Each turn's final answer, checked against the session up
			// to the end of that turn.
			last := map[int]int{} // turn → index of its last assistant text
			for i, e := range events {
				if e.Type == session.TypeAssistant && e.Message != nil && strings.TrimSpace(e.Message.Text()) != "" && e.Turn > 0 {
					last[e.Turn] = i
				}
			}
			for _, i := range last {
				answers++
				fs := numclaimFindings(events[i].Message.Text(), events[:i+1], sess.Meta.Cwd)
				if len(fs) == 0 {
					continue
				}
				withClaims++
				for _, f := range fs {
					claims++
					byStatus[f.Status]++
					if !f.OK() {
						unbacked++
					}
					samples = append(samples, string(f.Status)+" | "+f.Claim.Text+" | "+prefix(numclaimOneLine(numclaimSentence(events[i].Message.Text(), f.Claim.Text)), 140))
				}
			}
		}
	}
	sort.Strings(samples)
	for _, s := range samples {
		t.Log(s)
	}
	t.Logf("%d sessions, %d final answers, %d with numeric claims, %d claims, %d unbacked", sessions, answers, withClaims, claims, unbacked)
	for st, n := range byStatus {
		t.Logf("  %-16s %d", st, n)
	}
}

func numclaimSentence(text, needle string) string {
	i := strings.Index(text, needle)
	if i < 0 {
		return ""
	}
	start := max(strings.LastIndexAny(text[:i], "\n.!?")+1, 0)
	end := strings.IndexAny(text[i:], "\n")
	if end < 0 {
		end = len(text) - i
	}
	return strings.TrimSpace(text[start : i+end])
}

func numclaimOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
