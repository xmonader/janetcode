package scope

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/change"
)

// TestScreenRecordedRuns is the offline pre-screen for scope contracts:
// over recorded effbench campaigns, what share of the runs' diffs touch a
// file outside the scope inferred from the task, and are those runs the
// failing ones? It reads each campaign directory listed (colon-separated)
// in JANETCODE_SCOPE_RUNS (or every campaign under a directory listed
// there) and skips when that is unset:
//
//	JANETCODE_SCOPE_RUNS=docs/benchmarks/effbench/runs/jc-mimo-r13 go test ./internal/scope -run Screen -v
//
// For every janetcode run it rebuilds the tree the run started from (the
// session's recorded start snapshot, git archive'd out of the run's
// workspace, which must still be on disk), infers the contract from the
// prompt over that tree, and measures the run's provenance record
// (<session>.undo.json) against it. Shell-made edits are not in the
// record and are not counted.
func TestScreenRecordedRuns(t *testing.T) {
	env := os.Getenv("JANETCODE_SCOPE_RUNS")
	if env == "" {
		t.Skip("set JANETCODE_SCOPE_RUNS to effbench campaign directories")
	}
	type row struct {
		name        string
		pass        bool
		edited, out int
		creep       float64
		outside     []string
	}
	var rows []row
	skipped, tot, sz := 0, 0, 0
	var campaigns []string
	for _, c := range filepath.SplitList(env) {
		if _, err := os.Stat(filepath.Join(c, "results.jsonl")); err == nil {
			campaigns = append(campaigns, c)
			continue
		}
		// A directory of campaigns: every one that has results.
		more, _ := filepath.Glob(filepath.Join(c, "*", "results.jsonl"))
		for _, m := range more {
			campaigns = append(campaigns, filepath.Dir(m))
		}
	}
	for _, campaign := range campaigns {
		verdicts := scopeVerdicts(t, filepath.Join(campaign, "results.jsonl"))
		runs, _ := filepath.Glob(filepath.Join(campaign, "janetcode", "*"))
		sort.Strings(runs)
		for _, run := range runs {
			undo, _ := filepath.Glob(filepath.Join(run, "janetcode-sessions", "*.undo.json"))
			logs, _ := filepath.Glob(filepath.Join(run, "janetcode-sessions", "*.jsonl"))
			prompt, perr := os.ReadFile(filepath.Join(run, "prompt.txt"))
			if len(undo) != 1 || len(logs) != 1 || perr != nil {
				skipped++
				continue
			}
			cwd, snap := scopeStart(t, logs[0])
			start := t.TempDir()
			if cwd == "" || snap == "" || exec.Command("sh", "-c", "git -C '"+cwd+"' archive "+snap+" | tar -x -C '"+start+"'").Run() != nil {
				skipped++
				continue
			}
			c := Infer(start, string(prompt))
			var rec struct {
				Changes []change.Change `json:"changes"`
			}
			data, _ := os.ReadFile(undo[0])
			if json.Unmarshal(data, &rec) != nil {
				skipped++
				continue
			}
			r := Measure(c, cwd, rec.Changes)
			if r == nil {
				t.Logf("%s: no contract inferred", filepath.Base(run))
				skipped++
				continue
			}
			v, ok := verdicts[filepath.Base(run)]
			if !ok {
				skipped++
				continue
			}
			x := row{name: filepath.Base(filepath.Dir(filepath.Dir(run))) + "/" + filepath.Base(run), pass: v, edited: r.Edited, out: len(r.Outside), creep: r.Creep}
			for _, o := range r.Outside {
				x.outside = append(x.outside, o.Path)
			}
			rows = append(rows, x)
			tr, _ := walk(start, nil)
			tot += len(tr.files)
			for _, f := range tr.files {
				if c.Covers(f) {
					sz++
				}
			}
			t.Logf("%-40s %-4s tree=%3d scope=%3d edited=%2d outside=%d %v", x.name, map[bool]string{true: "PASS", false: "FAIL"}[v], len(tr.files), r.Size, r.Edited, x.out, x.outside)
		}
	}
	if len(rows) == 0 {
		t.Skipf("no runs could be screened (%d skipped)", skipped)
	}
	var withCreep, failWith, failWithout, passWith, passWithout int
	for _, r := range rows {
		switch {
		case r.out > 0 && r.pass:
			passWith++
		case r.out > 0:
			failWith++
		case r.pass:
			passWithout++
		default:
			failWithout++
		}
		if r.out > 0 {
			withCreep++
		}
	}
	t.Logf("%d runs screened, %d skipped; the contracts cover %d of the %d existing files the start trees hold (%.0f%%)", len(rows), skipped, sz, tot, 100*float64(sz)/float64(max(tot, 1)))
	t.Logf("runs touching a file outside the inferred scope: %d (%.0f%%)", withCreep, 100*float64(withCreep)/float64(len(rows)))
	t.Logf("fail rate with creep: %d/%d; without: %d/%d", failWith, failWith+passWith, failWithout, failWithout+passWithout)
}

// scopeVerdicts maps "<task>-s<seed>" to PASS for the janetcode arm.
func scopeVerdicts(t *testing.T, path string) map[string]bool {
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
			Task    string `json:"task"`
			Seed    int    `json:"seed"`
			Arm     string `json:"arm"`
			Verdict string `json:"verdict"`
			Valid   bool   `json:"valid"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Arm != "janetcode" || !r.Valid {
			continue
		}
		out[r.Task+"-s"+itoa(r.Seed)] = r.Verdict == "PASS"
	}
	return out
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// scopeStart reads a session log's meta line: its cwd and start snapshot.
func scopeStart(t *testing.T, path string) (cwd, snap string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	var m struct {
		Meta struct {
			Cwd   string `json:"cwd"`
			Start struct {
				Snap string `json:"snap"`
			} `json:"start"`
		} `json:"meta"`
	}
	if json.Unmarshal([]byte(first), &m) != nil {
		return "", ""
	}
	return m.Meta.Cwd, m.Meta.Start.Snap
}
