package agent

import (
	"fmt"
	"strings"
	"testing"
)

func failingLog(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "=== RUN   TestCase%d\n--- PASS: TestCase%d (0.00s)\n", i, i)
		if i == n/2 {
			b.WriteString("--- FAIL: TestMiddle (0.01s)\n    mid_test.go:42: want 3, got 4\n")
		}
	}
	b.WriteString("FAIL\nexit status 1\n[exit code 1]")
	return b.String()
}

func TestFailExcerptKeepsTheFailureAndArchivesTheLog(t *testing.T) {
	log := failingLog(200)
	if len(log) < failExcerptMin || len(log) > failExcerptMax {
		t.Fatalf("fixture size %d", len(log))
	}
	a := &Agent{Archive: NewArchive(t.TempDir())}
	out := a.stash("t1", "bash", "go test ./...", log)
	for _, want := range []string{"failure excerpt", "--- FAIL: TestMiddle", "want 3, got 4", "[exit code 1]", "exit code 1", "ctx-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if len(out) > failExcerptCap+1500 {
		t.Errorf("excerpt too long: %d", len(out))
	}
	if id := archiveNoteID(out); id != "ctx-1" {
		t.Errorf("archiveNoteID = %q", id)
	}
	_, body, err := a.Archive.Get("ctx-1")
	if err != nil || !strings.Contains(body, "TestCase0") {
		t.Fatalf("archive: %v", err)
	}
}

func TestFailExcerptBounds(t *testing.T) {
	log := failingLog(200)
	cases := map[string]struct {
		name, out string
		off       bool
		excerpt   bool
	}{
		"small":      {"bash", "FAIL: x\n", false, false},
		"no signal":  {"bash", strings.Repeat("all fine here\n", 600), false, false},
		"too large":  {"bash", failingLog(3000), false, false},
		"other tool": {"grep", log, false, false},
		"switched":   {"bash", log, true, false},
		"failing":    {"bash", log, false, true},
	}
	for name, c := range cases {
		a := &Agent{Archive: NewArchive(t.TempDir()), Off: map[string]bool{"failexcerpt": c.off}}
		got := strings.HasPrefix(a.stash("t", c.name, "cmd", c.out), "[failure excerpt")
		if got != c.excerpt {
			t.Errorf("%s: excerpt = %v, want %v", name, got, c.excerpt)
		}
	}
}
