// Command speccovscreen is the offline pre-screen of the spec-coverage
// matrix and of example harvesting (internal/speccov). With no model call
// it reads each effbench task's brief and public tests, builds the matrix,
// and over recorded effbench campaigns asks:
//
//   - of the hidden tests that failed a valid run, what share touch an
//     item the matrix flags as uncovered (the idea is dropped below 60%),
//     against the share of all hidden tests that do (the base rate a
//     matrix that flagged everything would score);
//
//   - how many tasks have examples Harvest can compile, and — with -run —
//     how many of those probes fail on the failed runs' workspaces (a
//     hidden failure an example would have caught) and on the reference
//     solution (a probe that misread its example).
//
//     speccovscreen -tasks ~/hero/code/hero_agent/verifications/longtasks/tasks \
//     docs/benchmarks/effbench/runs/jc-mimo-r8 ... jc-mimo-r13
//
// -run executes the compiled probes, under sh with a timeout, in copies of
// the workspaces made under a temporary directory; the originals are only
// read.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/speccov"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "speccovscreen:", err)
		os.Exit(1)
	}
}

// task is one effbench task, read.
type task struct {
	name    string
	dir     string
	matrix  speccov.Matrix
	probes  []speccov.Probe
	hidden  string            // every hidden test file, concatenated
	bodies  map[string]string // hidden test name → its source
	allTest []string          // every hidden test name, for the base rate
}

// prompt is what effbench gives the agent; the brief is the file it names.
const prompt = "Read BRIEF.md and implement the full task.\nPublic tests are in the workspace — make them pass."

func run(args []string) error {
	fl := flag.NewFlagSet("speccovscreen", flag.ContinueOnError)
	tasksDir := fl.String("tasks", "", "the effbench longtasks/tasks directory")
	runProbes := fl.Bool("run", false, "run the harvested probes on failed workspaces and on the reference solution")
	verbose := fl.Bool("v", false, "print every failure and its items")
	timeout := fl.Duration("timeout", 30*time.Second, "bound on one probe run (-run)")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if *tasksDir == "" {
		return errors.New("-tasks is required")
	}
	tasks := map[string]*task{}
	ents, err := os.ReadDir(*tasksDir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		seed := filepath.Join(*tasksDir, e.Name(), "seed")
		if _, err := os.Stat(filepath.Join(seed, "BRIEF.md")); err != nil {
			continue
		}
		tasks[e.Name()] = readTask(e.Name(), filepath.Join(*tasksDir, e.Name()))
	}
	names := make([]string, 0, len(tasks))
	for n := range tasks {
		names = append(names, n)
	}
	sort.Strings(names)

	fmt.Println("task                    items uncov unanch tests  probes(py/cli)")
	withProbes, items, uncov, examples, withExamples, compiled := 0, 0, 0, 0, 0, 0
	for _, n := range names {
		t := tasks[n]
		py, cli := 0, 0
		for _, p := range t.probes {
			if p.Shape == "python" {
				py++
			} else {
				cli++
			}
		}
		if len(t.probes) > 0 {
			withProbes++
		}
		items += len(t.matrix.Rows)
		ex := 0
		for _, r := range t.matrix.Rows {
			if r.Kind == speccov.Example {
				ex++
			}
		}
		examples += ex
		if ex > 0 {
			withExamples++
		}
		compiled += len(t.probes)
		uncov += t.matrix.Count(speccov.Uncovered)
		fmt.Printf("%-23s %5d %5d %6d %5d  %d/%d\n", n, len(t.matrix.Rows), t.matrix.Count(speccov.Uncovered),
			t.matrix.Count(speccov.Unanchored), len(t.matrix.Tests), py, cli)
	}
	fmt.Printf("\n%d tasks; %d items, %d (%.0f%%) flagged uncovered\n", len(names), items, uncov, pct(uncov, items))
	fmt.Printf("examples: %d in %d tasks' text; %d compiled into probes, in %d tasks\n", examples, withExamples, compiled, withProbes)

	// The base rate: every hidden test, failed or not.
	allTests, allOnFlag := 0, 0
	for _, t := range tasks {
		for _, name := range t.allTest {
			allTests++
			if onFlagged(t, t.bodies[name]) {
				allOnFlag++
			}
		}
	}

	type failure struct {
		campaign, arm, run, test string
		ws                       string
	}
	var fails []failure
	failedRuns := map[string][]string{} // workspace → task
	for _, camp := range fl.Args() {
		f, err := os.Open(filepath.Join(camp, "results.jsonl"))
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var r struct {
				Task, Arm, Verdict, Workspace string
				Seed                          int
				Valid                         bool
			}
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.Verdict != "FAIL" || !r.Valid || tasks[r.Task] == nil {
				continue
			}
			runName := fmt.Sprintf("%s-s%d", r.Task, r.Seed)
			grade, err := os.ReadFile(filepath.Join(camp, r.Arm, runName, "grade.log"))
			if err != nil {
				continue
			}
			failedRuns[r.Workspace] = append(failedRuns[r.Workspace], r.Task)
			names := failingTests(string(grade))
			if len(names) == 0 {
				names = []string{"?"}
			}
			for _, n := range names {
				fails = append(fails, failure{filepath.Base(camp), r.Arm, runName, n, r.Workspace})
			}
		}
		f.Close()
	}
	byArm := map[string][3]int{} // arm → failures, mapped to an item, on a flagged item
	// The same hidden test failing in several runs counts once here, so
	// one hard task does not carry the share on its own.
	distinct := map[string]map[string]bool{} // arm → task/test → on a flagged item
	for _, f := range fails {
		t := tasks[strings.TrimSuffix(f.run, f.run[strings.LastIndex(f.run, "-s"):])]
		body := t.bodies[f.test]
		c := byArm[f.arm]
		c[0]++
		touched := speccov.Touches(t.matrix, body)
		if body != "" && len(touched) > 0 {
			c[1]++
		}
		flagged := onFlagged(t, body)
		if flagged {
			c[2]++
		}
		byArm[f.arm] = c
		if distinct[f.arm] == nil {
			distinct[f.arm] = map[string]bool{}
		}
		distinct[f.arm][t.name+"/"+f.test] = flagged
		if *verbose {
			var ids []string
			for _, r := range touched {
				ids = append(ids, r.ID+":"+r.Status.String())
			}
			fmt.Printf("  %s %s %s %s flagged=%v %s\n", f.campaign, f.arm, f.run, f.test, flagged, strings.Join(ids, " "))
		}
	}
	fmt.Printf("\nhidden-test failures in valid FAIL runs (test level):\n")
	tot := [3]int{}
	for _, arm := range []string{"janetcode", "pi"} {
		c := byArm[arm]
		for i := range tot {
			tot[i] += c[i]
		}
		fmt.Printf("  %-9s %3d failures, %3d touch an item, %3d (%.0f%%) touch an uncovered item\n", arm, c[0], c[1], c[2], pct(c[2], c[0]))
	}
	fmt.Printf("  %-9s %3d failures, %3d touch an item, %3d (%.0f%%) touch an uncovered item\n", "both", tot[0], tot[1], tot[2], pct(tot[2], tot[0]))
	for _, arm := range []string{"janetcode", "pi"} {
		n, on := 0, 0
		for _, flagged := range distinct[arm] {
			n++
			if flagged {
				on++
			}
		}
		fmt.Printf("  %-9s %3d distinct failing hidden tests, %3d (%.0f%%) touch an uncovered item\n", arm, n, on, pct(on, n))
	}
	fmt.Printf("base rate: %d of %d hidden tests (%.0f%%) touch an uncovered item\n", allOnFlag, allTests, pct(allOnFlag, allTests))

	if !*runProbes {
		return nil
	}
	return screenProbes(tasks, failedRuns, *timeout)
}

// screenProbes runs each task's probes on the reference solution and on
// every failed workspace that still exists, in copies.
func screenProbes(tasks map[string]*task, failedRuns map[string][]string, timeout time.Duration) error {
	tmp, err := os.MkdirTemp("", "speccovscreen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	refFail, refPass, refNR := 0, 0, 0
	for name, t := range tasks {
		if len(t.probes) == 0 {
			continue
		}
		ref := filepath.Join(tmp, "ref-"+name)
		if copyTree(filepath.Join(t.dir, "seed"), ref) != nil || copyTree(filepath.Join(t.dir, "reference"), ref) != nil {
			continue
		}
		for _, p := range t.probes {
			switch out, res := runProbe(ref, p.Cmd, timeout); res {
			case "pass":
				refPass++
			case "fail":
				refFail++
				fmt.Printf("  reference fails %s %s: %s\n", name, p.ItemID, firstLines(out, 3))
			default:
				refNR++
			}
		}
	}
	fmt.Printf("\nprobes on the reference solution: %d pass, %d fail (misread examples), %d not runnable\n", refPass, refFail, refNR)
	runs, caught, wsMissing := 0, 0, 0
	for ws, names := range failedRuns {
		t := tasks[names[0]]
		if len(t.probes) == 0 {
			continue
		}
		runs++
		if _, err := os.Stat(ws); err != nil {
			wsMissing++
			continue
		}
		dst := filepath.Join(tmp, fmt.Sprintf("ws-%d", runs))
		if copyTree(ws, dst) != nil {
			wsMissing++
			continue
		}
		failing := 0
		for _, p := range t.probes {
			if _, res := runProbe(dst, p.Cmd, timeout); res == "fail" {
				failing++
			}
		}
		if failing > 0 {
			caught++
		}
		fmt.Printf("  %s: %d of %d probes fail\n", ws, failing, len(t.probes))
	}
	fmt.Printf("failed runs of tasks with probes: %d (%d workspaces gone); %d had a probe failing on them\n", runs, wsMissing, caught)
	return nil
}

// runProbe runs a probe in dir: pass, fail or nr (not runnable, timeout).
func runProbe(dir, cmd string, timeout time.Duration) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	c := exec.CommandContext(ctx, "bash", "-c", cmd)
	c.Dir = dir
	c.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	out, err := c.CombinedOutput()
	switch {
	case ctx.Err() != nil || strings.Contains(string(out), speccov.NotRunnable):
		return string(out), "nr"
	case err != nil:
		return string(out), "fail"
	}
	return string(out), "pass"
}

// readTask builds one task's matrix, probes and hidden-test index.
func readTask(name, dir string) *task {
	seed := filepath.Join(dir, "seed")
	text, _ := speccov.TaskText(seed, prompt)
	sp := speccov.Extract(text)
	tests := speccov.PublicTests(seed)
	t := &task{name: name, dir: dir, bodies: map[string]string{}}
	t.matrix = speccov.Cover(sp, tests)
	t.probes = speccov.Harvest(sp, speccov.Imports(sp, tests))
	var all strings.Builder
	_ = filepath.WalkDir(filepath.Join(dir, "tests"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() && strings.HasPrefix(d.Name(), ".") || d.IsDir() && d.Name() == "__pycache__" {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		all.Write(data)
		all.WriteByte('\n')
		for n, body := range testBodies(string(data)) {
			if n == "test_hidden_suite" {
				// The wrapper that runs a compiled language's hidden
				// tests is not a test of the task.
				continue
			}
			t.bodies[n] = body
			t.allTest = append(t.allTest, n)
		}
		return nil
	})
	t.hidden = all.String()
	return t
}

// onFlagged reports whether a test body touches an uncovered item.
func onFlagged(t *task, body string) bool {
	if body == "" {
		return false
	}
	for _, r := range speccov.Touches(t.matrix, body) {
		if r.Status == speccov.Uncovered {
			return true
		}
	}
	return false
}

// testDefRe starts a test in the languages effbench uses: a Python test
// function, a Go Test func, a Rust fn, a TS test()/it() call.
var testDefRe = regexp.MustCompile(`(?m)^\s*(?:def (test\w*)\s*\(|func (Test\w*)\s*\(|(?:pub )?fn (\w+)\s*\(|(?:test|it)\(\s*["'\x60]([^"'\x60]+)["'\x60])`)

// testBodies splits a test file into its tests: from one definition to
// the next.
func testBodies(src string) map[string]string {
	out := map[string]string{}
	locs := testDefRe.FindAllStringSubmatchIndex(src, -1)
	for i, loc := range locs {
		name := ""
		for g := 1; g <= 4; g++ {
			if loc[2*g] >= 0 {
				name = src[loc[2*g]:loc[2*g+1]]
				break
			}
		}
		end := len(src)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[name] = src[loc[0]:end]
	}
	return out
}

// failRes are the lines a grade log names a failed test on: pytest's
// section header, go test, cargo test, node's test runner.
var failRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^_{3,} (test\w*) _{3,}`),
	regexp.MustCompile(`--- FAIL: (Test\w+)`),
	regexp.MustCompile(`(?m)test (?:[\w:]+::)?(\w+) \.\.\. FAILED`),
	regexp.MustCompile(`(?m)^\s*(?:✖|not ok \d+ -) (.+?)\s*(?:\(\d|$)`),
}

// failingTests names the hidden tests a grade log reports failing.
func failingTests(log string) []string {
	var out []string
	seen := map[string]bool{"test_hidden_suite": true}
	for _, re := range failRes {
		for _, m := range re.FindAllStringSubmatch(log, -1) {
			n := strings.TrimSpace(m[1])
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	// go test -race interleaves its output, and a name can be cut short
	// in one of its two lines: keep the whole one.
	kept := out[:0]
	for _, n := range out {
		cut := false
		for _, o := range out {
			if o != n && strings.HasPrefix(o, n) {
				cut = true
			}
		}
		if !cut {
			kept = append(kept, n)
		}
	}
	return kept
}

// copyTree copies src into dst, skipping VCS and cache directories.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			if n := d.Name(); p != src && (n == ".git" || n == "node_modules" || n == "target" || n == "__pycache__") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func firstLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[:n]
	}
	return strings.Join(ls, " | ")
}
