package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/verify"
)

// The CONFLICT sentinel gives a model a legitimate way out of a task whose
// tests contradict it. Without one, the only ways to end such a task green
// are to edit the test or to special-case it, which is what the test
// guard and the ledger (testledger.go) are there to catch; ImpossibleBench
// measured an abort option cutting GPT-5's cheating from 54% to 9%. It
// works like the NEED protocol (need.go): no tool, one line of prompt, and
// a line in the final answer
//
//	CONFLICT: <test id> | <the task's words> | <why they cannot both hold>
//
// that the harness parses. The claim is checked, not taken: the cited
// test must exist in the project and fail when the turn ends. A confirmed
// conflict ends the turn without the gates that would send the model back
// to make that test pass, is recorded on the log and in the proof note,
// and ends a headless run with its own exit status (58). An unconfirmed
// one is sent back once, and after that the turn is judged as any other.
// It is opt-in (conflict_exit): it changes the prompt, and a model that
// reads it as an escape hatch from hard tasks costs passes.

// ConflictPrompt is the system prompt's line for the sentinel.
const ConflictPrompt = "- If a test contradicts the task, so that no code can satisfy both, do not change the test or special-case it: end your answer with a line `" + conflictFormat + "` and stop."

const conflictFormat = "CONFLICT: <test id> | <the task's words> | <why>"

// conflictRe matches the line, also in the bold or code span a model may
// wrap it in.
var conflictRe = regexp.MustCompile("(?im)^\\s*[*_`]*CONFLICT[*_`]*\\s*[:：]\\s*(.+?)\\s*$")

// parseConflict reads the first CONFLICT line of text.
func parseConflict(text string) *proof.Conflict {
	m := conflictRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	parts := strings.SplitN(strings.TrimRight(m[1], "`*_ "), "|", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	c := &proof.Conflict{Test: strings.Trim(strings.TrimSpace(parts[0]), "`'\"*_ "), Spec: strings.TrimSpace(parts[1]), Why: strings.TrimSpace(parts[2])}
	if c.Test == "" {
		return nil
	}
	return c
}

// conflictGate handles a CONFLICT line in the answer that ends a turn. It
// reports whether the turn goes back to the model; g.conflicted is set when
// the conflict was confirmed and the turn should end past the gates.
func (a *Agent) conflictGate(ctx context.Context, text string, g *turnGates, emit func(Event)) bool {
	if !a.ConflictExit || g.conflicted {
		return false
	}
	c := parseConflict(text)
	if c == nil {
		return false
	}
	a.checkConflict(ctx, c)
	if !c.Verified && !g.conflictNudged {
		g.conflictNudged = true
		a.logConflict(c)
		emit(Event{Kind: EvNotice, Text: "CONFLICT claimed but not confirmed: " + c.Note})
		a.appendHarnessText(fmt.Sprintf("[harness] You ended with CONFLICT on %s, but %s. Cite a test that exists and fails, by the name the test runner prints, or, if code can meet both the task and the tests, finish the work.", c.Test, c.Note))
		return true
	}
	a.logConflict(c)
	a.conflict = c
	if c.Verified {
		g.conflicted = true
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("CONFLICT: %s fails and the model says the task contradicts it (%s); ending the turn without making it pass", c.Test, c.Why)})
	}
	return false
}

// checkConflict fills in whether the cited test exists and fails.
func (a *Agent) checkConflict(ctx context.Context, c *proof.Conflict) {
	root := a.displayRoot()
	name, ok := findTest(root, c.Test)
	c.Exists = ok
	if !ok {
		c.Note = "no test file in the project holds " + c.Test
		return
	}
	commands, timeout := a.conflictCommands(root)
	if len(commands) == 0 {
		c.Note = "no test command ran in this turn to show it failing"
		return
	}
	res := (&verify.Runner{Config: verify.Config{Commands: commands, Timeout: timeout}, Cwd: root}).Run(ctx)
	c.Red = res.Failed != "" && strings.Contains(res.Output, name)
	c.Verified = c.Exists && c.Red
	switch {
	case res.Failed == "":
		c.Note = "it passes: " + strings.Join(commands, " && ") + " exits 0"
	case !c.Red:
		c.Note = "the failing run does not name it"
	}
}

// conflictCommands run the tests at the end: the project's verify gate,
// else the last test run of the turn, passing or not.
func (a *Agent) conflictCommands(root string) ([]string, int) {
	if gate, _ := a.VerifyGate(); gate.Enabled() && len(gate.Config.Commands) > 0 {
		return append([]string(nil), gate.Config.Commands...), gate.Config.Timeout
	}
	if a.Log == nil {
		return nil, 0
	}
	last := ""
	for _, e := range a.Log.Events() {
		if e.Turn != a.turnN || e.Type != session.TypeToolResult || e.Name != "bash" || refused(e) || neverRan(e) {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		// An inline script is the model's own check, not the project's
		// runner: re-run, it says nothing about the cited test, and on a
		// live run it "passed" and turned a true conflict away.
		if loc := scratchRe.FindStringIndex(cmd); loc != nil && !shell.IsTestRun(cmd[:loc[0]]) {
			continue
		}
		if shell.IsTestRun(cmd) {
			last = trimDisplayPipe(cmd)
		}
	}
	if last == "" {
		return nil, 0
	}
	return []string{last}, 300
}

// displayPipeRe matches a filter piped onto the end of a command only to
// shorten what it prints: `pytest -q 2>&1 | tail -20`.
var displayPipeRe = regexp.MustCompile(`\s*\|\s*(?:tail|head|grep|egrep|less|more|cat|tee|cut|sed|awk)\b[^|]*$`)

// trimDisplayPipe drops those filters. The pipeline's status is the last
// filter's, so a red run through `| tail` exits 0, and on a live run that
// turned a true conflict away as "it passes"; the filter can also cut the
// failing test's name out of what is left.
func trimDisplayPipe(cmd string) string {
	for {
		t := displayPipeRe.ReplaceAllString(cmd, "")
		if t == cmd || strings.TrimSpace(t) == "" {
			return cmd
		}
		cmd = t
	}
}

// findTest looks for the cited test in the project's test files and
// returns the name a runner would print for it. It takes the forms
// runners print: "tests/test_x.py::TestA::test_b", "test_b",
// "TestCache/evicts", "test_x.py".
func findTest(root, id string) (string, bool) {
	id = strings.TrimSpace(id)
	parts := strings.Split(id, "::")
	file, name := "", parts[len(parts)-1]
	if len(parts) > 1 || filepath.Ext(parts[0]) != "" && isTestFile(parts[0]) {
		file = parts[0]
	}
	if i := strings.IndexAny(name, "[( "); i > 0 {
		name = name[:i]
	}
	if i := strings.IndexByte(name, '/'); i > 0 && file == "" {
		name = name[:i] // a Go subtest: the parent is what the file holds
	}
	if file != "" && name == file {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(file)))
		return filepath.Base(file), err == nil
	}
	wordRe, err := regexp.Compile(`\b` + regexp.QuoteMeta(name) + `\b`)
	if err != nil || name == "" {
		return name, false
	}
	found, seen := false, 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if found {
			return filepath.SkipAll
		}
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > ledgerMaxFiles {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if !isTestFile(rel) || file != "" && rel != file && filepath.Base(rel) != filepath.Base(file) {
			return nil
		}
		if data, err := readSmall(p); err == nil && wordRe.Match(data) {
			found = true
		}
		return nil
	})
	return name, found
}

// logConflict records the claim on the session log, for the proof note.
func (a *Agent) logConflict(c *proof.Conflict) {
	raw, err := json.Marshal(c)
	if err != nil {
		return
	}
	a.logEvent(session.Event{Type: session.TypeConflict, Turn: a.turnN, Name: c.Test, Output: c.Why, IsError: !c.Verified, Extra: raw})
}

// Conflict is the CONFLICT the last turn ended on, or nil: confirmed when
// Verified, otherwise a claim the harness could not back.
func (a *Agent) Conflict() *proof.Conflict {
	if a.conflict == nil {
		return nil
	}
	c := *a.conflict
	return &c
}

// foldConflict is the turn's latest CONFLICT on the log, or nil.
func foldConflict(events []session.Event, turn int) *proof.Conflict {
	var c *proof.Conflict
	for _, e := range events {
		if e.Type != session.TypeConflict || e.Turn != turn {
			continue
		}
		var x proof.Conflict
		if json.Unmarshal(e.Extra, &x) == nil {
			c = &x
		}
	}
	return c
}
