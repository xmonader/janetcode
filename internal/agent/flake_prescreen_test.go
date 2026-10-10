package agent

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// TestFlakePrescreen is the zero-token pre-screen for flake quarantine
// (flake.go): over recorded sessions, how many red test runs were followed
// by an edit to code the failure did not name — the idea's own test — and,
// the sharper number, how many red runs the session itself showed to be
// non-deterministic: the same command run again with nothing edited in
// between that passed, or failed different tests.
//
// It reads directories of session logs, so it is skipped unless
// JANETCODE_FLAKE_PRESCREEN names them (a path list):
//
//	JANETCODE_FLAKE_PRESCREEN=docs/benchmarks/effbench/runs go test ./internal/agent -run FlakePrescreen -v
//
// "Nothing edited" counts only edit tools: a shell command that changed a
// file in between is invisible to it, so the loose flip count is an upper
// bound. The strict count also requires that no bash call other than a
// check ran in between.
func TestFlakePrescreen(t *testing.T) {
	dirs := os.Getenv("JANETCODE_FLAKE_PRESCREEN")
	if dirs == "" {
		t.Skip("set JANETCODE_FLAKE_PRESCREEN to directories of session logs")
	}
	var sessions, reds, editedAfter, unrelated, rerun, flipsLoose, flipsStrict int
	var examples []string
	for _, root := range filepath.SplitList(dirs) {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return nil
			}
			s, _, err := session.Open(filepath.Dir(p), strings.TrimSuffix(filepath.Base(p), ".jsonl"))
			if err != nil || s.Meta.Cwd == "" {
				return nil
			}
			sessions++
			for _, r := range flakePrescreenSession(s.Meta.Cwd, s.Events()) {
				reds++
				if r.nextEdit != "" {
					editedAfter++
					if !r.named {
						unrelated++
					}
				}
				if r.rerun {
					rerun++
				}
				if r.flip {
					flipsLoose++
					if r.strict {
						flipsStrict++
					}
					if len(examples) < 6 {
						examples = append(examples, fmt.Sprintf("%s: %s", s.Meta.ID, r.how))
					}
				}
			}
			return nil
		})
	}
	pct := func(n, d int) string {
		if d == 0 {
			return "n/a"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(d))
	}
	t.Logf("sessions %d, red test runs %d", sessions, reds)
	t.Logf("followed by an edit: %d; to a file the red output does not name: %d (%s of red runs, %s of those edited after)", editedAfter, unrelated, pct(unrelated, reds), pct(unrelated, editedAfter))
	t.Logf("rerun with no edit tool in between: %d; result flipped: %d (%s of red runs), %d with no other bash in between", rerun, flipsLoose, pct(flipsLoose, reds), flipsStrict)
	if len(examples) > 0 {
		t.Logf("  e.g. %s", strings.Join(examples, " | "))
	}
}

// flakeRed is one red test run of a session and what followed it.
type flakeRed struct {
	nextEdit string // the next file an edit tool changed, "" if none
	named    bool   // ... and the red output names it
	rerun    bool   // the same command ran again with no edit in between
	flip     bool   // ... and its result flipped
	strict   bool   // ... with no other bash call in between
	how      string
}

func flakePrescreenSession(root string, events []session.Event) []flakeRed {
	type open struct {
		i       int
		cmd     string
		output  string
		other   bool // a non-check bash ran since
		settled bool // the next run of cmd or an edit has been seen
	}
	var reds []flakeRed
	var opens []*open
	for _, e := range events {
		if e.Type != session.TypeToolResult || refused(e) {
			continue
		}
		if editTool(e.Name) && !e.IsError {
			p := perm.Subject(e.Name, []byte(e.Arguments))
			for _, o := range opens {
				if !o.settled {
					o.settled = true
					reds[o.i].nextEdit = p
					base := strings.TrimSuffix(path.Base(filepath.ToSlash(p)), path.Ext(p))
					reds[o.i].named = base != "" && strings.Contains(o.output, base)
				}
			}
			opens = nil
			continue
		}
		if e.Name != "bash" || neverRan(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		if !shell.IsCheck(cmd) {
			for _, o := range opens {
				o.other = true
			}
			continue
		}
		red := runFailed(e)
		for _, o := range opens {
			if o.settled || o.cmd != cmd {
				continue
			}
			o.settled = true
			r := &reds[o.i]
			r.rerun = true
			why := flakeFlip(o.output, flakePrescreenResult(red, e.Output), "on the same tree")
			if why != "" {
				r.flip, r.strict, r.how = true, !o.other, why
			}
		}
		if red && shell.IsTestRun(cmd) && !IsProbe(cmd) {
			reds = append(reds, flakeRed{})
			opens = append(opens, &open{i: len(reds) - 1, cmd: cmd, output: e.Output})
		}
	}
	return reds
}

func flakePrescreenResult(red bool, output string) probeResult {
	if red {
		return probeResult{outcome: probeFail, output: output}
	}
	return probeResult{outcome: probePass}
}
