// rolloutscreen runs the checkpoint-branch retry's and the verdict-gated
// second sample's offline pre-screens (internal/rollout's RunScreen) over
// recorded effbench campaigns. It reads; it never calls a model.
//
//	rolloutscreen docs/benchmarks/effbench/runs/jc-mimo-r{8,9,10,11,12,13}
//
// A campaign directory is what docs/benchmarks/effbench/run.py writes:
// results.jsonl with one graded row per run, and per run
// <arm>/<task>-s<seed>/ holding janetcode.out (the headless result
// object) and janetcode-sessions/ (the session log). Rows the benchmark
// marked invalid (tampered, or the wrong model served) are left out, as
// the benchmark's own pass counts leave them out.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/rollout"
	"github.com/xmonader/janetcode/internal/session"
)

func main() {
	arm := flag.String("arm", "janetcode", "the arm whose runs to read")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: rolloutscreen [-arm janetcode] <campaign dir>...")
		os.Exit(2)
	}
	var runs []rollout.Run
	skipped := 0
	for _, dir := range flag.Args() {
		rs, s, err := load(dir, *arm)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", dir, err)
			os.Exit(1)
		}
		runs, skipped = append(runs, rs...), skipped+s
	}
	if skipped > 0 {
		fmt.Printf("(%d invalid or unreadable rows left out)\n", skipped)
	}
	fmt.Print(rollout.RunScreen(runs).Text())
}

type row struct {
	Task    string `json:"task"`
	Seed    int    `json:"seed"`
	Arm     string `json:"arm"`
	Verdict string `json:"verdict"`
	Valid   bool   `json:"valid"`
}

// load reads one campaign's runs for arm.
func load(dir, arm string) (runs []rollout.Run, skipped int, err error) {
	f, err := os.Open(filepath.Join(dir, "results.jsonl"))
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		var r row
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Arm != arm {
			continue
		}
		if !r.Valid {
			skipped++
			continue
		}
		runDir := filepath.Join(dir, arm, fmt.Sprintf("%s-s%d", r.Task, r.Seed))
		events, err := events(filepath.Join(runDir, "janetcode-sessions"))
		if err != nil {
			skipped++
			continue
		}
		runs = append(runs, rollout.Run{
			Name:   filepath.Base(dir) + "/" + r.Task + "-s" + fmt.Sprint(r.Seed),
			Events: events,
			Err:    resultError(filepath.Join(runDir, "janetcode.out")),
			Pass:   r.Verdict == "PASS",
		})
	}
	return runs, skipped, sc.Err()
}

// events reads the one session a headless run leaves behind.
func events(dir string) ([]session.Event, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no session in %s", dir)
	}
	var all []session.Event
	for _, m := range matches {
		s, _, err := session.Open(dir, strings.TrimSuffix(filepath.Base(m), ".jsonl"))
		if err != nil {
			return nil, err
		}
		all = append(all, s.Events()...)
	}
	return all, nil
}

// resultError is the result object's error, "" when it exited clean or
// left no result at all.
func resultError(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var res struct {
		IsError bool   `json:"is_error"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &res) != nil || !res.IsError {
		return ""
	}
	if res.Error == "" {
		return "error"
	}
	return res.Error
}
