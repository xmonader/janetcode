// Command probescreen is the probe ledger's offline pre-screen (see
// internal/agent/probes.go): over recorded janetcode session logs, with no
// model call, it counts the runs whose turns ran at least one inline
// probe after editing — the material the ledger has to work with. The
// ledger is worth a live A/B only if that share is at least 30%.
//
//	probescreen docs/benchmarks/effbench/runs/jc-mimo-r13 ...
//
// Each argument is searched for */janetcode-sessions/*.jsonl, one run per
// session directory. With -classify it also runs every probe found, under
// sh with a timeout, against the session's start snapshot (exported from
// the workspace's git, which the log names) and against a copy of the
// workspace as the run left it, and says how many are discriminating
// (fail before, pass after) and how many vacuous (pass on both). That runs
// commands a model wrote, so it is off unless asked for.
package main

import (
	"context"
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

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/snapshot"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "probescreen:", err)
		os.Exit(1)
	}
}

// screened is one run's probes.
type screened struct {
	name   string
	meta   session.Meta
	probes []agent.Probe
}

func run(args []string) error {
	fs := flag.NewFlagSet("probescreen", flag.ContinueOnError)
	classify := fs.Bool("classify", false, "run each probe against the start snapshot and the final workspace")
	verbose := fs.Bool("v", false, "print every probe")
	timeout := fs.Duration("timeout", 60*time.Second, "bound on one probe run (-classify)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("give one or more directories of effbench runs")
	}
	for _, dir := range fs.Args() {
		runs, err := screen(dir)
		if err != nil {
			return err
		}
		with, total := 0, 0
		for _, r := range runs {
			total += len(r.probes)
			if len(r.probes) > 0 {
				with++
			}
			if *verbose {
				for _, p := range r.probes {
					fmt.Printf("  %s: %s\n", r.name, oneLine(p.Key))
				}
			}
		}
		pct := 0.0
		if len(runs) > 0 {
			pct = 100 * float64(with) / float64(len(runs))
		}
		fmt.Printf("%s: %d runs, %d with at least one inline probe after an edit (%.0f%%), %d probes\n", dir, len(runs), with, pct, total)
		if *classify {
			c := classifyAll(runs, *timeout)
			fmt.Printf("  classified %d of %d probes: %d discriminating (%d of them only because a name was missing before), %d vacuous, %d fail on the final tree, %d not runnable; %d runs with a discriminating probe\n",
				c.discriminating+c.vacuous+c.failAfter, total, c.discriminating, c.missing, c.vacuous, c.failAfter, c.unrunnable, c.runsWithDisc)
		}
	}
	return nil
}

// screen reads every session log under dir.
func screen(dir string) ([]screened, error) {
	var logs []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(p, ".jsonl") && filepath.Base(filepath.Dir(p)) == "janetcode-sessions" {
			logs = append(logs, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(logs)
	var out []screened
	for _, p := range logs {
		s, _, err := session.Open(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".jsonl"))
		if err != nil {
			return nil, err
		}
		r := screened{name: filepath.Base(filepath.Dir(filepath.Dir(p))), meta: s.Meta}
		events := s.Events()
		turns := map[int]bool{}
		for _, e := range events {
			if e.Turn > 0 && !turns[e.Turn] {
				turns[e.Turn] = true
				r.probes = append(r.probes, agent.InlineProbes(events, e.Turn, s.Meta.Cwd)...)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

type counts struct {
	discriminating, vacuous, failAfter, unrunnable, runsWithDisc int
	// missing counts the discriminating probes whose run on the old code
	// failed because a name the change added did not exist yet: they
	// show the API is there, a weaker claim than a rule holding.
	missing int
}

// missingRe is an interpreter saying a name does not exist.
var missingRe = regexp.MustCompile(`ImportError|ModuleNotFoundError|AttributeError|NameError|is not a function|is not a constructor|does not provide an export|has no exported member|Cannot find module`)

// classifyAll runs each run's probes before and after.
func classifyAll(runs []screened, timeout time.Duration) counts {
	var c counts
	ctx := context.Background()
	for _, r := range runs {
		if len(r.probes) == 0 {
			continue
		}
		before, after, cleanup, err := trees(ctx, r.meta)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", r.name, err)
			c.unrunnable += len(r.probes)
			continue
		}
		disc := false
		for _, p := range r.probes {
			_, a := runProbe(ctx, p.Key, r.meta.Cwd, after, timeout)
			out, b := runProbe(ctx, p.Key, r.meta.Cwd, before, timeout)
			switch {
			case a == errRun || b == errRun:
				c.unrunnable++
			case a != nil:
				c.failAfter++
			case b != nil:
				c.discriminating++
				disc = true
				if missingRe.MatchString(out) {
					c.missing++
				}
			default:
				c.vacuous++
			}
		}
		if disc {
			c.runsWithDisc++
		}
		cleanup()
	}
	return c
}

// trees exports the start snapshot and copies the workspace as it was
// left, so nothing runs in the recorded workspace itself.
func trees(ctx context.Context, m session.Meta) (before, after string, cleanup func(), err error) {
	if m.Start == nil || m.Start.Snap == "" {
		return "", "", nil, errors.New("the log has no start snapshot")
	}
	repo, err := snapshot.Open(ctx, m.Cwd)
	if err != nil {
		return "", "", nil, fmt.Errorf("workspace %s: %w", m.Cwd, err)
	}
	base, err := os.MkdirTemp("", "probescreen-")
	if err != nil {
		return "", "", nil, err
	}
	cleanup = func() { os.RemoveAll(base) }
	before, after = filepath.Join(base, "before"), filepath.Join(base, "after")
	os.Mkdir(before, 0o755)
	if err := repo.Export(ctx, snapshot.Snap{Commit: m.Start.Snap}, before); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := os.CopyFS(after, os.DirFS(m.Cwd)); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return before, after, cleanup, nil
}

// errRun is a probe that could not be run at all: it timed out or its
// interpreter is missing.
var errRun = errors.New("not runnable")

// runProbe runs a probe in dir with the workspace path pointed at it,
// and returns its combined output.
func runProbe(ctx context.Context, cmd, root, dir string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if root != "" {
		cmd = strings.ReplaceAll(cmd, root, dir)
	}
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Dir = dir
	out, err := c.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return string(out), nil
	case ctx.Err() != nil, errors.As(err, &ee) && ee.ExitCode() == 127, !errors.As(err, &ee):
		return string(out), errRun
	}
	return string(out), err
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}
