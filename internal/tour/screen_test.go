package tour

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
)

// TestScreenRecordedRuns is the offline pre-screen for the review tour:
// over recorded sessions, what share of the final diffs' changed lines
// does Classify call mechanical? Below 20% there is little for the tour
// to collapse. It reads every *.undo.json (a session's provenance record)
// under JANETCODE_TOUR_RUNS and skips when that is unset, so it costs
// nothing in an ordinary test run:
//
//	JANETCODE_TOUR_RUNS=docs/benchmarks/effbench/runs/jc-mimo-r13/janetcode go test ./internal/tour -run Screen -v
//
// Each session is toured as one review (Build over all its changes), so
// a line rewritten five times counts once, the way a reviewer reads it.
// Shell-made changes are not in the record and are not counted.
func TestScreenRecordedRuns(t *testing.T) {
	root := os.Getenv("JANETCODE_TOUR_RUNS")
	if root == "" {
		t.Skip("set JANETCODE_TOUR_RUNS to a directory of recorded sessions")
	}
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(p) == ".json" && filepath.Ext(p[:len(p)-5]) == ".undo" {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	byClass := map[Class]int{}
	total, sessions := 0, 0
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Changes []change.Change `json:"changes"`
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if len(rec.Changes) == 0 {
			continue
		}
		sessions++
		tr := Build(rec.Changes, nil)
		lines, mech := 0, 0
		for _, it := range append(tr.Items, tr.Mechanical...) {
			byClass[it.Class] += it.Lines()
			lines += it.Lines()
			if it.Class.Mechanical() {
				mech += it.Lines()
			}
		}
		total += lines
		rel, _ := filepath.Rel(root, f)
		t.Logf("%-70s %5d lines  %4d mechanical", rel, lines, mech)
	}
	if total == 0 {
		t.Skip("no changed lines under " + root)
	}
	mech := byClass[Format] + byClass[Rename]
	t.Logf("%d sessions, %d changed lines", sessions, total)
	for _, c := range []Class{Behaviour, Test, Comment, Format, Rename} {
		t.Logf("  %-9s %6d  %5.1f%%", c, byClass[c], 100*float64(byClass[c])/float64(total))
	}
	t.Logf("mechanical share: %.1f%% (kill below 20%%)", 100*float64(mech)/float64(total))
}
