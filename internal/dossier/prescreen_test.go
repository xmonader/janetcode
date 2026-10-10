package dossier

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/session"
)

// TestScreenRecordedRuns is the offline look at what a failure dossier
// would have handed over for the failed runs of recorded effbench
// campaigns. The idea's own test — does a person or a second run fix the
// issue faster with the dossier than without — needs people or live
// runs; this only says whether there is anything to hand over: a red
// check to reproduce (narrowed to its tests or not), hypotheses with the
// evidence against them, a best state before the end. It reads every
// campaign under JANETCODE_DOSSIER_RUNS (a campaign directory or a
// directory of them) and skips when that is unset:
//
//	JANETCODE_DOSSIER_RUNS=docs/benchmarks/effbench/runs go test ./internal/dossier -run Screen -v
func TestScreenRecordedRuns(t *testing.T) {
	env := os.Getenv("JANETCODE_DOSSIER_RUNS")
	if env == "" {
		t.Skip("set JANETCODE_DOSSIER_RUNS to effbench campaign directories")
	}
	results, _ := filepath.Glob(filepath.Join(env, "results.jsonl"))
	more, _ := filepath.Glob(filepath.Join(env, "*", "results.jsonl"))
	results = append(results, more...)
	sort.Strings(results)
	var failed, withRepro, narrowed, withHyp, hyps, ruledOut, withBest int
	for _, res := range results {
		campaign := filepath.Dir(res)
		for name := range dossierFailed(res) {
			logs, _ := filepath.Glob(filepath.Join(campaign, "janetcode", name, "janetcode-sessions", "*.jsonl"))
			if len(logs) != 1 {
				continue
			}
			events, cwd := dossierEvents(logs[0])
			var rec struct {
				Changes []change.Change `json:"changes"`
			}
			if data, err := os.ReadFile(strings.TrimSuffix(logs[0], ".jsonl") + ".undo.json"); err == nil {
				_ = json.Unmarshal(data, &rec)
			}
			d := Build(name, "", "FAIL", cwd, events, rec.Changes)
			failed++
			if d.Repro != nil {
				withRepro++
				if d.Repro.Narrowed {
					narrowed++
				}
			}
			if len(d.Hypotheses) > 0 {
				withHyp++
			}
			hyps += len(d.Hypotheses) + d.Dropped
			for _, h := range d.Hypotheses {
				if h.Verdict == "ruled out" || h.Verdict == "worse" {
					ruledOut++
				}
			}
			if d.Best != nil {
				withBest++
			}
			last := "-"
			if d.LastRed != nil {
				last = textClip(d.LastRed.Command, 50)
			}
			t.Logf("%-34s %-28s hyps=%2d best=%v repro=%v last red: %s", filepath.Base(campaign), name, len(d.Hypotheses)+d.Dropped, d.Best != nil, d.Repro != nil, last)
		}
	}
	if failed == 0 {
		t.Skip("no failed janetcode runs with a session log")
	}
	t.Logf("%d failed runs: %d with a red check to reproduce (%d narrowed to its tests), %d with hypotheses (%d in all, %d ruled out by a red check), %d with a best state before the end",
		failed, withRepro, narrowed, withHyp, hyps, ruledOut, withBest)
}

// dossierFailed is the valid janetcode runs a campaign's results mark
// FAIL, by "<task>-s<seed>".
func dossierFailed(path string) map[string]bool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r struct {
			Task, Arm, Verdict string
			Seed               int
			Valid              bool
		}
		if json.Unmarshal(sc.Bytes(), &r) == nil && r.Arm == "janetcode" && r.Valid && r.Verdict == "FAIL" {
			b, _ := json.Marshal(r.Seed)
			out[r.Task+"-s"+string(b)] = true
		}
	}
	return out
}

// dossierEvents reads a session log's events and its cwd.
func dossierEvents(path string) ([]session.Event, string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, ""
	}
	var evs []session.Event
	cwd := ""
	for _, line := range strings.Split(string(data), "\n") {
		var head struct {
			Type string `json:"type"`
			Meta struct {
				Cwd string `json:"cwd"`
			} `json:"meta"`
		}
		if json.Unmarshal([]byte(line), &head) != nil {
			continue
		}
		if head.Type == "meta" {
			cwd = head.Meta.Cwd
			continue
		}
		var e session.Event
		if head.Type == "event" && json.Unmarshal([]byte(line), &e) == nil {
			evs = append(evs, e)
		}
	}
	return evs, cwd
}

func textClip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
