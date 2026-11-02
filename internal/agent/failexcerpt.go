package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// A failing command's mid-sized log (4 to 60 KiB) is answered with a
// deterministic failure excerpt instead of the head/tail clip; the whole
// log is archived and stays retrievable through expand_output.
//
// Ported from hero_agent (tool_output.rs, shell_evidence_receipt, the
// SoL-Pi reducer analogue). hero measured that a plain head/tail clip hid
// the failing assertion in the elided middle: 7.3% of results truncated
// on the 2026-07-31 probe10 arm (10 of 137), and the escape hatch,
// expand_context, used 0 times against those 10. So the lines that carry
// the failure are kept by rule, not by position, and the rest is one
// expand_output away. Not an LLM summary: the same log gives the same
// excerpt.
//
// Deviation: hero also raised its general inline budget 6k to 60k. Here
// bash stays at 12 KiB (inlineBudget) for output with no failure signal;
// no janetcode measurement backs a raise, and the excerpt already covers
// the case hero's numbers were about (a failing log). Off:
// JANETCODE_OFF=failexcerpt.

const (
	failExcerptMin = 4 << 10
	failExcerptMax = 60 << 10
	failExcerptCap = 3500
	failTailLines  = 12
)

var failSignals = []string{
	"error[", "failed", "failure", "assert", "panic", "traceback",
	"--- fail", "test result:", "short test summary", "error:", "fatal:",
}

// hasFailureSignal reports whether log looks like a failing run.
func hasFailureSignal(log string) bool {
	l := strings.ToLower(log)
	for _, s := range failSignals {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// failureExcerpt keeps every signal line with one line of context after
// it, then the last failTailLines lines (the exit code is among them).
func failureExcerpt(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	keep := make([]bool, len(lines))
	for i, ln := range lines {
		l := strings.ToLower(ln)
		hit := strings.HasPrefix(l, "e       ")
		for _, s := range failSignals {
			if strings.Contains(l, s) {
				hit = true
				break
			}
		}
		if hit {
			keep[i] = true
			if i+1 < len(lines) {
				keep[i+1] = true
			}
		}
	}
	for i := max(0, len(lines)-failTailLines); i < len(lines); i++ {
		keep[i] = true
	}
	var b strings.Builder
	gap := false
	for i, ln := range lines {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && b.Len() > 0 {
			b.WriteString("…\n")
		}
		gap = false
		fmt.Fprintf(&b, "%d: %s\n", i+1, ln)
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(out) > failExcerptCap {
		out = textfmt.TruncateTo(out, failExcerptCap)
	}
	return out
}

// failExcerpt is the stash result for a failing bash log, or "" when the
// output is not one (or the archive refuses it).
func (a *Agent) failExcerpt(toolUse, name, subject, out string) string {
	if name != "bash" || a.Off["failexcerpt"] || a.Archive == nil ||
		len(out) < failExcerptMin || len(out) > failExcerptMax || !hasFailureSignal(out) {
		return ""
	}
	id, err := a.Archive.PutSubject(toolUse, name, subject, out)
	if err != nil {
		return ""
	}
	exit := "exit code 0 or not reported"
	if code, failed := tool.CommandFailed(subject, out); failed || code != 0 {
		exit = fmt.Sprintf("exit code %d", code)
	}
	ex := failureExcerpt(out)
	lines := strings.Count(out, "\n") + 1
	return fmt.Sprintf("[failure excerpt of a %s, %d-line log (%s; sha256 %s), chosen by rule, not summarized: lines with error/fail/assert/panic signals plus the line after, then the last %d lines, each prefixed with its line number. The full output is saved as %s: expand_output {\"id\":%q,\"grep\":\"<regexp>\"} finds lines in it, {\"id\":%q,\"offset\":<line>,\"limit\":<lines>} reads a slice.]\n%s",
		textfmt.Bytes(int64(len(out))), lines, exit, outputDigest(out), failTailLines, id, id, id, tool.EscapeDelimiters(ex))
}
