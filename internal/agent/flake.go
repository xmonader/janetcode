package agent

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Flake quarantine (FlakeQuarantine): when the model's own test run comes
// back red, the harness runs the same command again, once on the tree as
// it is and — when this turn already saw the command's result on the tree
// as it was before the first change — once on that tree, exported from
// the undo checkpoint's snapshot as the probe ledger does. A result that
// flips between two runs on one tree, red to green or a different set of
// failing tests, is noise: the model is told the test is
// non-deterministic, so it does not "fix" code that the red run never
// implicated. A run that fails the same way twice says nothing and adds
// nothing to the output. The gate's flaky verdict lives in the optional
// Jev triage and asks a model; this one is deterministic and asks none.
//
// It is opt-in (config flake_quarantine), because it reruns commands the
// model wrote while the model waits, and nothing has measured it on
// effbench; JANETCODE_OFF=flaky turns it off again. Reruns go through the
// bash tool (runProbe), so the sandbox and the permission rules hold them,
// and a run that took longer than flakeMaxRun is not rerun at all.

// Limits: a red run slower than flakeMaxRun is not rerun (the rerun is
// bounded by probeTimeout, and a run that slow would rather time out than
// say anything), and one turn reruns at most maxFlakePerTurn red runs.
const (
	flakeMaxRun     = 45 * time.Second
	maxFlakePerTurn = 2
)

// flakeState is the session's flake record.
type flakeState struct {
	mu sync.Mutex
	// turn and n count this turn's checks; done keys a command with the
	// mutation count it was checked at, so an unchanged rerun of the
	// same red state is not checked twice.
	turn int
	n    int
	done map[string]bool
	// flaky holds the commands found non-deterministic this session,
	// with what showed it; a later red run of one is told so again
	// without another rerun.
	flaky map[string]string
}

// claim reports whether the red run keyed k may be checked in turn, and
// counts it.
func (s *flakeState) claim(turn int, k string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turn != turn || s.done == nil {
		s.turn, s.n, s.done = turn, 0, map[string]bool{}
	}
	if s.done[k] || s.n >= maxFlakePerTurn {
		return false
	}
	s.done[k] = true
	s.n++
	return true
}

func (s *flakeState) known(cmd string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flaky[cmd]
}

func (s *flakeState) mark(cmd, why string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flaky == nil {
		s.flaky = map[string]string{}
	}
	s.flaky[cmd] = why
}

// SetFlakeQuarantine turns flake quarantine on or off for the turns that
// follow.
func (a *Agent) SetFlakeQuarantine(on bool) {
	a.featMu.Lock()
	a.FlakeQuarantine = on
	a.featMu.Unlock()
}

// FlakeQuarantineOn is FlakeQuarantine, safe to read while a turn runs,
// and off when JANETCODE_OFF names flaky.
func (a *Agent) FlakeQuarantineOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.FlakeQuarantine && !a.Off["flaky"]
}

// flakeBash is the note for a bash call's result: something only when the
// call ran tests, came back red, and a rerun showed the result is not
// deterministic. output is the call's result before it was archived.
func (a *Agent) flakeBash(ctx context.Context, name string, input []byte, output string, isErr bool, dur time.Duration, emit func(Event)) string {
	if name != "bash" || isErr || !a.FlakeQuarantineOn() || a.readOnly() || a.DryRunOn() {
		return ""
	}
	root := a.displayRoot()
	cmd := shell.TrimWorkspaceCd(shell.CommandOf(string(input)), root)
	e := session.Event{Name: name, Arguments: string(input), Output: output}
	if !shell.IsTestRun(cmd) || !runFailed(e) || neverRan(e) || IsProbe(cmd) {
		return ""
	}
	if why := a.flaky.known(cmd); why != "" {
		return flakeNote(why, true)
	}
	if dur > flakeMaxRun || !a.flaky.claim(a.turnN, fmt.Sprintf("%s\x00%d", cmd, a.mutations)) {
		return ""
	}
	emit(Event{Kind: EvNotice, Text: "flake check: rerunning the red test command"})
	why := ""
	again := a.runProbe(ctx, shell.CommandOf(string(input)), "")
	if ctx.Err() != nil {
		return ""
	}
	if again.outcome != probeInconclusive {
		why = flakeFlip(output, again, "on the code as it is now")
	}
	if why == "" && a.mutations > 0 {
		if earlier, ok := a.flakePreEditResult(cmd, root); ok {
			if pre := a.preEditTree(ctx); pre != "" {
				res := a.runProbe(ctx, shell.CommandOf(string(input)), pre)
				os.RemoveAll(pre)
				if ctx.Err() != nil {
					return ""
				}
				if res.outcome != probeInconclusive {
					why = flakeFlip(earlier, res, "on the code as it was before this turn's first change")
				}
			}
		}
	}
	summary := "deterministic: the rerun failed the same way"
	if why != "" {
		summary = "non-deterministic: " + why
		a.flaky.mark(cmd, why)
	}
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "flaky", Output: checkLabel(cmd) + ": " + summary})
	emit(Event{Kind: EvNotice, Text: "flake check: " + summary})
	if why == "" {
		return ""
	}
	return flakeNote(why, false)
}

// flakePreEditResult is the output of the turn's last run of cmd that
// came before its first change, and whether there was one: what the
// pre-edit rerun is compared against.
func (a *Agent) flakePreEditResult(cmd, root string) (string, bool) {
	if a.Log == nil {
		return "", false
	}
	out, ok := "", false
	for _, e := range a.Log.Events() {
		if e.Type != session.TypeToolResult || e.Turn != a.turnN || refused(e) {
			continue
		}
		if editTool(e.Name) && !e.IsError {
			break
		}
		if e.Name == "bash" && !neverRan(e) && shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root) == cmd {
			out, ok = e.Output, true
			if !runFailed(e) {
				out = ""
			}
		}
	}
	return out, ok
}

// flakeFlip compares a run's output (first, "" for a green run) with a
// rerun of the same command on the same tree and says how the result
// flipped, or "" when it did not or the two cannot be told apart. Two
// red runs differ only when both name their failing tests and the names
// differ: a timing line or a seed in the output is not a flip.
func flakeFlip(first string, again probeResult, where string) string {
	firstRed := first != ""
	switch {
	case firstRed && again.outcome == probePass:
		return "it failed and then passed, unchanged, " + where
	case !firstRed && again.outcome == probeFail:
		return "it passed and then failed, unchanged, " + where
	case !firstRed:
		return ""
	}
	a, b := flakeFailingTests(first), flakeFailingTests(again.output)
	if len(a) == 0 || len(b) == 0 || strings.Join(a, "\n") == strings.Join(b, "\n") {
		return ""
	}
	return fmt.Sprintf("it failed twice, unchanged, %s, but with different tests failing (%s, then %s)", where, flakeList(a), flakeList(b))
}

func flakeList(names []string) string {
	if len(names) > 3 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d more", len(names)-3)
	}
	return strings.Join(names, ", ")
}

// flakeTestRes name a failing test in the output of the common runners:
// go test, pytest, cargo test, jest and vitest, node --test and TAP.
var flakeTestRes = []*regexp.Regexp{
	regexp.MustCompile(`(?m)^\s*--- FAIL: (\S+)`),
	regexp.MustCompile(`(?m)^FAILED (\S+::\S+)`),
	regexp.MustCompile(`(?m)^(\S+::\S+) FAILED\b`),
	regexp.MustCompile(`(?m)^test (\S+) \.\.\. FAILED\b`),
	regexp.MustCompile(`(?m)^\s*[✕×] (.+?)(?: \(\d+(?:\.\d+)? ?m?s\))?\s*$`),
	regexp.MustCompile(`(?m)^not ok \d+ - (.+?)\s*(?:#.*)?$`),
}

// flakeFailingTests are the distinct failing tests a red output names,
// sorted.
func flakeFailingTests(output string) []string {
	seen := map[string]bool{}
	var out []string
	for _, re := range flakeTestRes {
		for _, m := range re.FindAllStringSubmatch(output, -1) {
			if n := strings.TrimSpace(m[1]); n != "" && !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// flakeNote is what the model reads under a red run found flaky.
func flakeNote(why string, earlier bool) string {
	when := "A rerun by the harness shows"
	if earlier {
		when = "Earlier in this session a rerun by the harness showed"
	}
	return "\n\n[harness] " + when + " this test command is non-deterministic: " + textfmt.FirstLine(why, 300) +
		". This red result may be noise rather than something the code does wrong. Do not change code only to make it pass; if the failure is in what you changed, fix that, otherwise say the test is flaky and move on."
}
