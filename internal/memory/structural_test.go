package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAboutMatchesConcernsExactly(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "a", Description: "about compact", Body: "x", Source: "auto", Concerns: []string{"internal/agent/compact.go"}})
	s.Put(Entry{Name: "b", Description: "about prune", Body: "y", Source: "auto", Concerns: []string{"internal/agent/prune.go"}})
	got := s.About([]string{"internal/agent/compact.go"})
	if len(got) != 1 || got[0].Name != "a" {
		t.Fatalf("about = %+v", got)
	}
	// A shared directory is not a match: the question is exact.
	if got := s.About([]string{"internal/agent/other.go"}); len(got) != 0 {
		t.Fatalf("segment match leaked: %+v", got)
	}
}

func TestHistoryWalksSupersession(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "old", Description: "use tabs", Body: "x", Source: "user"})
	s.Put(Entry{Name: "new", Description: "use spaces", Body: "y", Source: "user"})
	if _, err := s.Close("old", "new", time.Now()); err != nil {
		t.Fatal(err)
	}
	h := s.History("old")
	if len(h) != 2 || h[0].Name != "old" || h[1].Name != "new" {
		t.Fatalf("history = %+v", h)
	}
	cur := s.Current()
	if len(cur) != 1 || cur[0].Name != "new" {
		t.Fatalf("current = %+v", cur)
	}
	if s.History("never") != nil {
		t.Fatal("unknown name should be nil")
	}
}

// The record pairs a gate's failure with its later pass into one line
// the model can act on, and lists entries about the changed files
// under their own heading so a correction names what it replaces.
func TestSleepRecordPairsGatesAndSplitsEntriesByConcern(t *testing.T) {
	in := SleepInput{
		SessionID: "sess1",
		Facts: []SleepFact{
			{Seq: 7, Kind: "verify", Name: "make test", Text: "VERSION and version.go must match"},
			{Seq: 9, Kind: "verify-pass", Name: "make test"},
			{Seq: 11, Kind: "verify-pass", Name: "go vet"}, // never failed: not a fact
		},
		Touched: []string{"VERSION", "version.go"},
	}
	existing := []Entry{
		{Name: "ver-rule", Type: "project", Description: "old version rule", Concerns: []string{"version.go"}},
		{Name: "unrelated", Type: "project", Description: "something else", Concerns: []string{"main.go"}},
	}
	f := &fakeProvider{reply: "NONE"}
	if _, err := Sleep(context.Background(), f, "m", in, existing); err != nil {
		t.Fatal(err)
	}
	ask := f.req.Messages[len(f.req.Messages)-1].Text()
	for _, want := range []string{
		"[7] (verify make test) VERSION and version.go must match",
		"[9] (check make test) passed after failing at [7]",
		"about the files this session changed",
	} {
		if !strings.Contains(ask, want) {
			t.Fatalf("missing %q in:\n%s", want, ask)
		}
	}
	if strings.Contains(ask, "[11]") {
		t.Fatalf("a pass with no failure is not a record line:\n%s", ask)
	}
	i, j := strings.Index(ask, "- ver-rule"), strings.Index(ask, "Other existing entries")
	k := strings.Index(ask, "- unrelated")
	if i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Fatalf("entries not split by concern:\n%s", ask)
	}
}
