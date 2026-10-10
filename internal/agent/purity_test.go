package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/purity"
	"github.com/xmonader/janetcode/internal/tool"
)

// purityAgent is an agent with the real bash tool, a purity store of its
// own and an approver that says yes and counts. It skips without strace.
func purityAgent(t *testing.T, calls ...string) (*Agent, string, *recordApprover) {
	t.Helper()
	if purity.Detect() == nil {
		t.Skip("no usable strace here")
	}
	var rs []*llm.Response
	for i, c := range calls {
		rs = append(rs, toolCall("p"+string(rune('a'+i)), "bash", c))
	}
	rs = append(rs, text("done"))
	ap := &recordApprover{answer: Allow}
	a, root := newAgent(t, &fake{responses: rs}, ap)
	a.Tools.Add(tool.Bash{Root: root})
	a.ObservedPurity, a.Purity = true, purity.Open(true, t.TempDir())
	return a, root, ap
}

// puritySeed records n clean runs of command, as if they had been watched.
func puritySeed(a *Agent, command string, n int) {
	for i := 0; i < n; i++ {
		a.Purity.Observe(a.displayRoot(), command, purity.Verdict{Pure: true, Exec: []string{"/usr/bin/bash", "/usr/bin/cat"}})
	}
}

func purityNotices(evs []Event) []string {
	var out []string
	for _, e := range evs {
		if e.Kind == EvNotice && strings.HasPrefix(e.Text, "observed purity") {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestPurityPromotesAReadOnlyCommandAfterWatchedRuns(t *testing.T) {
	c := `{"command":"cat a.txt"}`
	a, _, ap := purityAgent(t, c, c, c, c, c)
	evs, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != purity.PromoteAfter {
		t.Fatalf("asked %d times, want %d (then promoted)", len(ap.calls), purity.PromoteAfter)
	}
	if n := purityNotices(*evs); len(n) != 1 || !strings.Contains(n[0], "runs without asking") {
		t.Fatalf("notices %q", n)
	}
	if r := a.Purity.Get(a.displayRoot(), "cat a.txt"); r.Pure != 5 || !r.Promoted() {
		t.Fatalf("record %+v", r)
	}
}

func TestPurityNeverPromotesAWritingCommand(t *testing.T) {
	c := `{"command":"cat a.txt > b.txt"}`
	a, _, ap := purityAgent(t, c, c, c, c)
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 4 {
		t.Fatalf("a writing command asked %d of 4 times", len(ap.calls))
	}
	if r := a.Purity.Get(a.displayRoot(), "cat a.txt > b.txt"); !strings.Contains(r.Impure, "b.txt") {
		t.Fatalf("record %+v", r)
	}
}

// A promoted command that writes is caught on that run and asks again
// after it, for good.
func TestPurityDemotesAPromotedCommandThatWrites(t *testing.T) {
	c := `{"command":"cat a.txt > c.txt"}`
	a, root, ap := purityAgent(t, c, c)
	puritySeed(a, "cat a.txt > c.txt", purity.PromoteAfter)
	evs, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 1 {
		t.Fatalf("asked %d times, want the second run only", len(ap.calls))
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); err != nil {
		t.Fatal("the promoted run did not run")
	}
	if n := purityNotices(*evs); len(n) != 1 || !strings.Contains(n[0], "asks again") || !strings.Contains(n[0], "c.txt") {
		t.Fatalf("notices %q", n)
	}
}

func TestPurityTaintedSessionAsksAndRecordsNothing(t *testing.T) {
	c := `{"command":"cat a.txt"}`
	a, _, ap := purityAgent(t, c)
	puritySeed(a, "cat a.txt", purity.PromoteAfter)
	a.taintTracker().Add("fetch_url https://example.com", "page")
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 1 {
		t.Fatalf("a tainted session ran a promoted command unasked (%d asks)", len(ap.calls))
	}
	if r := a.Purity.Get(a.displayRoot(), "cat a.txt"); r.Pure != purity.PromoteAfter {
		t.Fatalf("a tainted run was recorded: %+v", r)
	}
}

func TestPurityOffSwitchAndBackgroundAsk(t *testing.T) {
	c := `{"command":"cat a.txt"}`
	a, _, ap := purityAgent(t, c, `{"command":"cat a.txt","background":true}`)
	puritySeed(a, "cat a.txt", purity.PromoteAfter)
	a.Off = map[string]bool{"purity": true}
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if len(ap.calls) != 2 {
		t.Fatalf("with purity off asked %d of 2 times", len(ap.calls))
	}
	// Switch on again: a background run is never watched, so never
	// approved on the strength of the foreground ones.
	delete(a.Off, "purity")
	bg := llm.Block{Name: "bash", Input: []byte(`{"command":"cat a.txt","background":true}`)}
	if a.purityAllows(bg) {
		t.Fatal("a background run was promoted")
	}
	if !a.purityAllows(llm.Block{Name: "bash", Input: []byte(c)}) {
		t.Fatal("the seeded command is not promoted")
	}
	a.SetObservedPurity(false)
	if a.purityAllows(llm.Block{Name: "bash", Input: []byte(c)}) {
		t.Fatal("promoted with the setting off")
	}
}
