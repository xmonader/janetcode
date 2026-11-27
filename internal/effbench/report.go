package effbench

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Run is one finished run with its metered totals.
type Run struct {
	Result
	Totals Totals
}

// LoadRuns reads every <campaign>/<arm>/<run>/result.json (+ meter.jsonl)
// under root. root may be one campaign directory or a directory of them.
func LoadRuns(root string) ([]Run, error) {
	files, err := filepath.Glob(filepath.Join(root, "*", "*", "result.json"))
	if err != nil {
		return nil, err
	}
	more, _ := filepath.Glob(filepath.Join(root, "*", "*", "*", "result.json"))
	files = append(files, more...)
	sort.Strings(files)
	var runs []Run
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var r Result
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		recs, _ := readMeter(filepath.Join(filepath.Dir(f), "meter.jsonl"))
		runs = append(runs, Run{Result: r, Totals: Sum(recs)})
	}
	return runs, nil
}

func readMeter(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var recs []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var r Record
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			recs = append(recs, r)
		}
	}
	return recs, sc.Err()
}

type agg struct {
	arm     string
	n, pass int
	wall    float64
	tot     Totals
	skipped string
}

// WriteReport renders the markdown table, one section per model. refTPS > 0
// fixes the common decode speed; otherwise it is the pooled speed of refArm
// in that section (falling back to all arms pooled).
func WriteReport(w io.Writer, runs []Run, refArm string, refTPS float64) {
	byModel := map[string][]Run{}
	var models []string
	for _, r := range runs {
		if _, ok := byModel[r.Model]; !ok {
			models = append(models, r.Model)
		}
		byModel[r.Model] = append(byModel[r.Model], r)
	}
	sort.Strings(models)
	fmt.Fprintln(w, "# effbench results")
	fmt.Fprintln(w, "\n\"Adj. time\" recomputes each agent's wall time at a common decode speed: wall - decode time + output / reference speed, so runs from different days compare. \"Fresh input\" is input not served from the prompt cache. \"Served\" is the model the response said answered; MISMATCH means a different vendor than the one asked for, and such a row is not a result for the model named. Rows are sorted by adj. time.")
	for _, m := range models {
		rs := byModel[m]
		arms := map[string]*agg{}
		var order []string
		var all Totals
		for _, r := range rs {
			a := arms[r.Arm]
			if a == nil {
				a = &agg{arm: r.Arm}
				arms[r.Arm] = a
				order = append(order, r.Arm)
			}
			if r.Skipped != "" {
				a.skipped = r.Skipped
				continue
			}
			a.n++
			if r.Pass {
				a.pass++
			}
			a.wall += r.WallSec
			a.tot = merge(a.tot, r.Totals)
			all = merge(all, r.Totals)
		}
		ref := refTPS
		if ref <= 0 {
			if a := arms[refArm]; a != nil {
				ref = a.tot.TPS()
			}
		}
		if ref <= 0 {
			ref = all.TPS()
		}
		sort.SliceStable(order, func(i, j int) bool {
			ai, aj := arms[order[i]], arms[order[j]]
			if (ai.n == 0) != (aj.n == 0) {
				return ai.n > 0
			}
			return AdjustedSeconds(ai.wall, ai.tot, ref) < AdjustedSeconds(aj.wall, aj.tot, ref)
		})
		fmt.Fprintf(w, "\n## %s\n\nReference decode speed: %.1f tok/s.\n\n", m, ref)
		fmt.Fprintln(w, "| Agent | Passed | Wall | Adj. time | Calls | Input | Cached | Fresh input | Output | Served | Flag |")
		fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|")
		for _, name := range order {
			a := arms[name]
			if a.n == 0 {
				fmt.Fprintf(w, "| %s | skipped | | | | | | | | | %s |\n", name, a.skipped)
				continue
			}
			cached := "-"
			if a.tot.Input > 0 {
				cached = fmt.Sprintf("%d%%", 100*a.tot.Cached/a.tot.Input)
			}
			flag := ""
			if a.tot.Mismatch {
				flag = "MISMATCH"
			} else if a.tot.Calls == 0 {
				flag = "no metered calls"
			} else if a.tot.Errors > 0 {
				flag = fmt.Sprintf("%d upstream errors", a.tot.Errors)
			}
			fmt.Fprintf(w, "| %s | %d/%d | %s | %s | %d | %s | %s | %s | %s | %s | %s |\n", name, a.pass, a.n,
				secs(a.wall), secs(AdjustedSeconds(a.wall, a.tot, ref)), a.tot.Calls,
				num(a.tot.Input), cached, num(a.tot.Fresh), num(a.tot.Output), strings.Join(a.tot.Served, ", "), flag)
		}
	}
}

func merge(a, b Totals) Totals {
	a.Calls += b.Calls
	a.Input += b.Input
	a.Cached += b.Cached
	a.Fresh += b.Fresh
	a.Output += b.Output
	a.Errors += b.Errors
	a.ModelMS += b.ModelMS
	a.DecodeMS += b.DecodeMS
	for _, s := range b.Served {
		a.Served = addUniq(a.Served, s)
	}
	a.Mismatch = a.Mismatch || b.Mismatch
	return a
}

func secs(s float64) string { return fmt.Sprintf("%.0f s", s) }

func num(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.2fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprint(n)
}
