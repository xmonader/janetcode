package agent

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/proof"
)

// securityFormat is the one line per finding both security reviewers are
// told to write, so their reports can be merged and ranked without a
// model call.
const securityFormat = `Report each finding on ONE line, exactly:
[SEVERITY] path:line — the problem — how an attacker reaches it — the fix
SEVERITY is one of CRITICAL, HIGH, MEDIUM, LOW. Only report problems this diff introduces or touches, with a concrete path to exploit; no style notes, no generic advice. If you find nothing, write exactly: NO FINDINGS`

// SecurityReviewTasks are the subagents /security-review fans out over
// diff: one follows untrusted input to where it is used, one checks who
// may do what and what leaks. focus narrows what they look at.
func SecurityReviewTasks(focus, diff string) []TaskInput {
	if focus == "" {
		focus = "the current uncommitted changes"
	}
	briefs := []struct{ desc, extra string }{
		{"security: input", "You are a security reviewer. Trace untrusted input (request fields, files, environment, command-line arguments, model or tool output, anything read from the network) to where the diff uses it. Look for command, SQL, template and log injection; path traversal and symlink escapes; SSRF and open redirects; unsafe deserialization and YAML/pickle loading; XSS and missing output encoding; unbounded reads and regular expressions an attacker can make slow."},
		{"security: access", "You are a security reviewer. Look for missing or wrong authentication and authorization checks, privilege escalation, secrets and credentials committed or logged, weak or misused cryptography (hand-rolled, ECB, fixed IVs, math/rand for tokens, non-constant-time compares), insecure defaults (TLS verification off, permissive CORS, world-writable files), and race conditions that bypass a check (TOCTOU)."},
	}
	out := make([]TaskInput, 0, len(briefs))
	for _, b := range briefs {
		out = append(out, TaskInput{
			Description: b.desc,
			Agent:       "explore",
			Prompt:      b.extra + "\n\nReview " + focus + ". Read the surrounding code where the diff alone does not show whether input is trusted.\n\n" + securityFormat + "\n\n" + diff,
		})
	}
	return out
}

// findingRe reads "[HIGH] text", "HIGH: text", "**HIGH** — text" and
// "- HIGH - text", with an optional list marker, but not a sentence that
// merely starts with the word ("High-level summary").
var findingRe = regexp.MustCompile(`(?i)^\s*(?:[-*•]\s*|\d+[.)]\s*)?(?:\*\*)?(?:\[(critical|high|medium|low)\]|(critical|high|medium|low)(?:\*\*)?\s*(?:[:|—–]|-\s))(?:\*\*)?\s*(.+)$`)

var severityRank = map[string]int{"CRITICAL": 0, "HIGH": 1, "MEDIUM": 2, "LOW": 3}

// SecurityFinding is one ranked line of a security review.
type SecurityFinding struct {
	Severity string
	Text     string
}

// RankSecurityFindings merges the reviewers' reports into one list, most
// severe first, dropping a finding two reviewers both wrote. unparsed is
// what a report said outside the finding format, so a reviewer that
// ignored it is still read rather than lost.
func RankSecurityFindings(reports []string) (findings []SecurityFinding, unparsed []string) {
	seen := map[string]bool{}
	for _, r := range reports {
		var loose []string
		parsed := false
		for _, line := range strings.Split(r, "\n") {
			m := findingRe.FindStringSubmatch(line)
			if m == nil {
				if t := strings.TrimSpace(line); t != "" && !strings.EqualFold(t, "NO FINDINGS") {
					loose = append(loose, t)
				}
				continue
			}
			parsed = true
			sev, text := strings.ToUpper(m[1]+m[2]), strings.TrimSpace(m[3])
			key := strings.ToLower(text)
			if seen[key] {
				continue
			}
			seen[key] = true
			findings = append(findings, SecurityFinding{Severity: sev, Text: text})
		}
		if !parsed && len(loose) > 0 {
			unparsed = append(unparsed, strings.Join(loose, "\n"))
		}
	}
	sort.SliceStable(findings, func(i, j int) bool {
		return severityRank[findings[i].Severity] < severityRank[findings[j].Severity]
	})
	return findings, unparsed
}

// SecurityReport is the text /security-review shows and hands the model:
// a count by severity, the ranked findings, and anything a reviewer wrote
// outside the format. failed names reviewers that did not finish.
func SecurityReport(reports []string, failed []string) string {
	findings, unparsed := RankSecurityFindings(reports)
	var b strings.Builder
	counts := map[string]int{}
	for _, f := range findings {
		counts[f.Severity]++
	}
	var parts []string
	for _, s := range []string{"CRITICAL", "HIGH", "MEDIUM", "LOW"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], strings.ToLower(s)))
		}
	}
	switch {
	case len(findings) == 0 && len(unparsed) == 0:
		b.WriteString("security review: no findings")
	case len(findings) == 0:
		b.WriteString("security review: no ranked findings")
	default:
		fmt.Fprintf(&b, "security review: %d findings (%s)", len(findings), strings.Join(parts, ", "))
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "\n%-8s %s", f.Severity, f.Text)
	}
	for _, u := range unparsed {
		b.WriteString("\n\nunranked, from a reviewer that did not use the format:\n" + u)
	}
	for _, f := range failed {
		b.WriteString("\n\nreviewer did not finish: " + f)
	}
	return b.String()
}

// RunSecurityReview runs the security reviewers in parallel through the
// task tool and returns their findings merged and ranked.
func RunSecurityReview(ctx context.Context, task *Task, focus, diff string) string {
	tasks := SecurityReviewTasks(focus, diff)
	reports := make([]string, len(tasks))
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, in := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = task.Start(ctx, in)
		}()
	}
	wg.Wait()
	var failed []string
	for i, err := range errs {
		if err != nil {
			failed = append(failed, tasks[i].Description+" ("+err.Error()+")")
		}
	}
	return SecurityReport(reports, failed)
}

// SecurityReviewShared is how a finished security review is handed to the
// model for the next turn.
func SecurityReviewShared(report string) string {
	return "A security review of the current diff found:\n<security-review>\n" + report + "\n</security-review>"
}

// ProofAttacher attaches this session's proof note to a commit made in
// dir, reporting whether one was attached. It is what /commit calls once
// it has committed.
func (a *Agent) ProofAttacher(dir string) func(context.Context, string) bool {
	return func(ctx context.Context, sha string) bool {
		if a == nil || a.Off["proof"] {
			return false
		}
		root, err := proof.Root(ctx, dir)
		if err != nil {
			return false
		}
		_, err = a.AttachProof(ctx, root, sha)
		return err == nil
	}
}
