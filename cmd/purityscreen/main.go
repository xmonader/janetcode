// Command purityscreen is the offline pre-screen for auto-approval from
// observed purity (internal/purity, internal/agent/purity.go): over
// recorded session logs, with no model call, it replays every bash call
// in order and counts the approval prompts promotion would have saved.
//
//	purityscreen ~/.local/share/janetcode/sessions
//	purityscreen -by-task docs/benchmarks/effbench/runs/jc-mimo-r*
//
// Every bash call is counted as a prompt, as it is in the default mode.
// A call is saved when the same command line, in the same repository,
// already had purity.PromoteAfter earlier runs counted pure and
// promotable. A recorded log holds no trace, so "pure" stands in for the
// monitor's verdict two ways: the classifier's shell.SafeRead (the
// screen's number), and "every repeat counts" (an upper bound with no
// purity filter at all). The programs a command runs, for the
// interpreter bar, are its argv[0]s as written.
//
// A repository is the session's working directory; -by-task makes it the
// effbench task instead (the run directory without its -sN seed), since
// each effbench run works in a fresh copy of the same task.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/purity"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "purityscreen:", err)
		os.Exit(1)
	}
}

// call is one recorded bash call.
type call struct {
	repo, command string
}

// log is one session's bash calls, in order.
type log struct {
	created string
	calls   []call
}

func run(args []string) error {
	fl := flag.NewFlagSet("purityscreen", flag.ContinueOnError)
	byTask := fl.Bool("by-task", false, "key a repository by effbench task, not by working directory")
	if err := fl.Parse(args); err != nil {
		return err
	}
	if fl.NArg() == 0 {
		return errors.New("give one or more directories of session logs")
	}
	var logs []log
	for _, dir := range fl.Args() {
		ls, err := read(dir, *byTask)
		if err != nil {
			return err
		}
		logs = append(logs, ls...)
	}
	sort.SliceStable(logs, func(i, j int) bool { return logs[i].created < logs[j].created })
	r := screen(logs)
	fmt.Printf("sessions %d (with bash %d), bash calls (prompts in the default mode) %d\n", r.sessions, r.withBash, r.calls)
	fmt.Printf("saved, classifier as the purity stand-in: %d (%.1f%% of prompts, %.2f per session with bash)\n", r.saved, pct(r.saved, r.calls), per(r.saved, r.withBash))
	fmt.Printf("saved, upper bound (every repeat counts): %d (%.1f%%)\n", r.upper, pct(r.upper, r.calls))
	return nil
}

// result is the screen's count.
type result struct {
	sessions, withBash, calls, saved, upper int
}

// screen replays the calls in order through a promotion record per
// repository and command line.
func screen(logs []log) result {
	type rec struct{ pure, any int }
	recs := map[call]*rec{}
	var r result
	for _, l := range logs {
		r.sessions++
		if len(l.calls) > 0 {
			r.withBash++
		}
		for _, c := range l.calls {
			c.command = purity.Key(c.command)
			r.calls++
			x := recs[c]
			if x == nil {
				x = &rec{}
				recs[c] = x
			}
			if x.pure >= purity.PromoteAfter {
				r.saved++
			}
			if x.any >= purity.PromoteAfter {
				r.upper++
			}
			x.any++
			if pureStandIn(c.command) {
				x.pure++
			} else {
				x.pure = -1 << 30 // seen writing, or barred: never promoted
			}
		}
	}
	return r
}

// pureStandIn is the screen's guess at the monitor's verdict: the
// classifier calls the command read-only, and it runs no interpreter
// and nothing by a relative path.
func pureStandIn(command string) bool {
	if !shell.SafeRead(command) {
		return false
	}
	v := purity.Verdict{Pure: true, Exec: []string{"/bin/sh"}}
	for _, argv := range shell.Argvs(command) {
		if len(argv) == 0 {
			continue
		}
		p := argv[0]
		if !strings.Contains(p, "/") {
			p = "/usr/bin/" + p
		}
		v.Exec = append(v.Exec, p)
	}
	ok, _ := purity.Promotable(v, "")
	return ok
}

var seedSuffix = regexp.MustCompile(`-s\d+$`)

// read loads every *.jsonl under dir. It parses the lines itself rather
// than opening the session, so it never takes a session's lock or writes
// beside a log it was only asked to read.
func read(dir string, byTask bool) ([]log, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []log
	for _, p := range paths {
		l, err := readOne(p, byTask)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, l)
	}
	return out, nil
}

func readOne(path string, byTask bool) (log, error) {
	f, err := os.Open(path)
	if err != nil {
		return log{}, err
	}
	defer f.Close()
	var l log
	repo := ""
	if byTask {
		repo = seedSuffix.ReplaceAllString(filepath.Base(filepath.Dir(filepath.Dir(path))), "")
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var head struct {
			Type string        `json:"type"`
			Meta *session.Meta `json:"meta"`
		}
		line := sc.Bytes()
		if json.Unmarshal(line, &head) != nil {
			continue
		}
		if head.Type == "meta" && head.Meta != nil {
			l.created = head.Meta.Created.Format("2006-01-02T15:04:05.000000000")
			if !byTask {
				repo = head.Meta.Cwd
			}
			continue
		}
		var e session.Event
		if head.Type != "event" || json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.Type == session.TypeToolCall && e.Name == "bash" {
			if cmd := shell.CommandOf(e.Arguments); cmd != "" && !background(e.Arguments) {
				l.calls = append(l.calls, call{repo: repo, command: cmd})
			}
		}
	}
	return l, sc.Err()
}

// background reports a call the monitor would never see.
func background(args string) bool {
	var in struct{ Background, Interactive bool }
	_ = json.Unmarshal([]byte(args), &in)
	return in.Background || in.Interactive
}

func pct(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func per(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}
