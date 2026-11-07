package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// A headless turn whose tests pass and whose own check keeps failing at the
// same place is told once to decide which of the two is wrong. On effbench
// (Kimi, syn-kimi-pi2 a-catalog-paging-s1) the public tests passed at call
// 13; the model then spent about 20 calls alternating an inline
// `python - <<'EOF'` check that failed the same assertion (a sort tie-break
// order the task never states), a probe printing orders, and small edits.
// The hidden tests never failed on sorting, so the check was the wrong one.
// The wrap-up note fired, and was ignored because the model's own check was
// still red; the prompt rule "decide once whether the code or the check is
// wrong" was ignored too. A note injected on a tool result is not.
// JANETCODE_OFF=owncheck turns it off.

// ownCheckFails is how many failures of one own check, at one place, the
// note waits for: two is a retry after a fix, three is a loop.
const ownCheckFails = 3

// ownState is whether the turn has been given the note.
type ownState struct{ fired bool }

var (
	// tbFileRe is a traceback frame; the last one is the innermost.
	tbFileRe = regexp.MustCompile(`File "([^"]+)", line (\d+)`)
	// excRe is the exception line a traceback ends with.
	excRe = regexp.MustCompile(`^([A-Za-z_][\w.]*(?:Error|Exception|Exit|Interrupt))\b`)
)

// failureSignature names where an own check failed, short and stable
// across retries, and whether the output shows a failure at all. It reads
// the last Python traceback in the output, not the exit code: the model
// chains its check with the test run (`python - <<EOF ... EOF` then
// `python -m pytest -q`), the tests' exit 0 hides the traceback, and in
// the measured session five of the eight failing runs read as passes by
// exit code. The signature is the exception type and the file of the last
// frame ("AssertionError at <stdin>"); for a named file the line goes in
// too ("AssertionError at cat.py line 40"). The message is left out: an
// assertion that prints the values it compared changes it on every edit.
// The line of an inline script is left out as well: the model rewrites the
// script on every retry and the same failing check moved between lines 10,
// 11 and 13. Output without a traceback (node, a shell script) counts only
// when the command exited non-zero (failed), and its signature is its last
// non-empty line. "" means no failure.
func failureSignature(out string, failed bool) string {
	if i := strings.LastIndex(out, "Traceback (most recent call last):"); i >= 0 {
		var frame []string
		for _, l := range strings.Split(out[i:], "\n")[1:] {
			if m := tbFileRe.FindStringSubmatch(l); m != nil {
				frame = m
				continue
			}
			if m := excRe.FindStringSubmatch(l); m != nil && frame != nil {
				file := frame[1]
				if strings.HasPrefix(file, "<") {
					return fmt.Sprintf("%s at %s", m[1], file)
				}
				return fmt.Sprintf("%s at %s line %s", m[1], filepath.Base(file), frame[2])
			}
		}
	}
	if !failed {
		return ""
	}
	lines := strings.Split(out, "\n")
	for j := len(lines) - 1; j >= 0; j-- {
		if l := strings.TrimSpace(lines[j]); l != "" && !reExitCode.MatchString(l) {
			return textfmt.TruncateTo(l, 100)
		}
	}
	return ""
}

// ownCheckScan reads the turn's events for the two things the note needs.
// Project is a test command (shell.IsCheck) that is not the model's own;
// own is a script the model wrote for the occasion: written inline, created
// this turn, or outside the root (the verdict fold's scratch predicate).
// green is true when the latest project run came back passing; edits do not
// clear it, because the model edits while chasing its own check and a
// broken edit turns the next project run red anyway. Failures of own
// checks are counted per signature since the project last passed, edits and
// other calls in between not resetting them. It returns the most frequent
// signature and its count.
func ownCheckScan(events []session.Event, turn int, root string) (green bool, sig string, n int) {
	created := map[string]bool{}
	counts := map[string]int{}
	for _, e := range events {
		if e.Turn != turn || e.Type != session.TypeToolResult || e.CallID == "" || refused(e) {
			continue
		}
		if e.Name == "write_file" && strings.HasPrefix(e.Output, "Created ") {
			if p := argPathOf(e.Arguments); p != "" {
				created[p] = true
			}
		}
		if e.Name != "bash" {
			continue
		}
		cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root)
		if neverRan(e) || unfinished(e) {
			continue
		}
		own := scratchScript(cmd) || runsScript(cmd, func(w string) bool {
			if created[strings.TrimPrefix(w, "./")] || created[w] {
				return true
			}
			return filepath.IsAbs(w) && root != "" && !strings.HasPrefix(w, root+string(filepath.Separator))
		})
		switch {
		case own:
			if s := failureSignature(e.Output, runFailed(e)); s != "" {
				counts[s]++
			}
		case shell.IsCheck(cmd):
			green = !runFailed(e)
			clear(counts)
		}
	}
	for s, c := range counts {
		if c > n {
			sig, n = s, c
		}
	}
	return green, sig, n
}

// ownCheckNote is the note for the step that just ran, or "" when it does
// not apply. Like wrapUpNote it does not mark itself given: the caller does
// once the note has a result to ride on.
func (a *Agent) ownCheckNote() string {
	if !a.OwnCheck || a.Off["owncheck"] || a.own.fired || a.reviewSteps >= 0 || a.DryRunOn() || a.Log == nil {
		return ""
	}
	green, sig, n := ownCheckScan(a.Log.Events(), a.turnN, a.displayRoot())
	if !green || n < ownCheckFails {
		return ""
	}
	return fmt.Sprintf("[harness] The task's tests pass, and your own check has failed %d times at the same place (%s). Decide now whether your check or the code is wrong, by what the task says: if the task does not state that rule, drop the check. Say which in one line, then finish or fix the code once.", n, textfmt.TruncateTo(sig, 100))
}
