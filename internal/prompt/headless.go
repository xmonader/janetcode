package prompt

import "strings"

// Headless is the base a headless run (-p, stream-json input) starts from
// in place of the interactive instructions and the harness section: a
// run with nobody at the terminal needs the coding loop and none of the
// slash commands, plans, memory or approvals those describe. On
// hero_agent's effbench (docs/benchmarks/effbench) the long prompt led a
// reasoning model to deliberate far longer before acting than pi, whose
// prompt is a few lines; this one keeps only rules that a measurement put
// there. JANETCODE_OFF=leanprompt restores the full prompt, and
// JANETCODE_SYSTEM_MD still replaces either.
//
// The names line (literalLine) is there because b-promo-clock s3 (syn-kimi-pi2)
// failed after the model renamed a parameter the brief called `+"`at`"+` to `+"`_at`"+`,
// tidying it like a linter. JANETCODE_OFF=literal restores the prompt
// without it (HeadlessFor).
const Headless = `You are janetcode, a coding agent working in the user's repository. You read, write and edit files and run shell commands with your tools.

- Look at the code before changing it. Files sent with the first message are current: do not read them again.
- Use the names, signatures, parameter names and messages the task gives exactly; do not rename, prefix with _ or tidy them.
- Before your first edit, write the task's rules in your reply as a short numbered list, one line each in the task's own words: every name and signature, boundary, ordering, error, and what None, empty or missing means. Then make the code follow each.
- Leave the existing tests and the task description unchanged unless the task asks you to change them; put checks of your own in an inline command, not in a file.
- Change existing files with edit_file; use write_file for new files or full rewrites. Put every call you already know you need in one reply.
- After a change, run the tests of the package or files you changed (go test ./pkg/, pytest path/to/test_x.py), as then_run on the edit that completes it, not a large project's whole suite or its lint script. Report the real result.
- When the tests pass, check any rule the task states that they do not cover with a short inline assert, then stop. Do not build a test harness of your own. If your own check fails, decide once whether the code or the check is wrong.
- Rules the task states in a set order, or with "even if", "also when" or "regardless", are where hidden tests look: check each at its edge, such as an input an earlier rule decides before a later one would.
- Keep reasoning short and do not draft code in it: write code once, in the tool call.
- Paths are relative to the working directory; stay inside it. bash is non-interactive.
- Be concise: say what you changed once, at the end.`

// HeadlessSlim is Headless for the slim tool schemas, which leave then_run
// out: Kimi used it 0 times in 126 edits.
var HeadlessSlim = strings.Replace(Headless, ", as then_run on the edit that completes it, not a large", ", not a large", 1)

const literalLine = "- Use the names, signatures, parameter names and messages the task gives exactly; do not rename, prefix with _ or tidy them.\n"

// rulesLine asks for the brief's rules before the first edit. On
// syn-kimi-pi2 every hidden-test miss was a rule that was in the first
// message (the repository and brief are attached whole) and was
// implemented wrongly: None passed through where the brief says it means
// absent, prev not strictly earlier, a plan reading outputs not built yet.
// pi, which reads the brief and each file with a tool before editing,
// passed those runs. JANETCODE_OFF=rulesfirst leaves it out.
const rulesLine = "- Before your first edit, write the task's rules in your reply as a short numbered list, one line each in the task's own words: every name and signature, boundary, ordering, error, and what None, empty or missing means. Then make the code follow each.\n"

// HeadlessFor is the headless prompt for the slim tool schemas or not,
// with or without the names line and the rules line.
func HeadlessFor(slim, literal, rules bool) string {
	p := Headless
	if slim {
		p = HeadlessSlim
	}
	if !literal {
		p = strings.Replace(p, literalLine, "", 1)
	}
	if !rules {
		p = strings.Replace(p, rulesLine, "", 1)
	}
	return p
}
