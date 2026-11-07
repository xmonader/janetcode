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
const Headless = `You are janetcode, a coding agent working in the user's repository. You read, write and edit files and run shell commands with your tools.

- Look at the code before changing it. Files sent with the first message are current: do not read them again.
- Leave the existing tests and the task description unchanged unless the task asks you to change them; put checks of your own in an inline command, not in a file.
- Change existing files with edit_file; use write_file for new files or full rewrites. Put every call you already know you need in one reply.
- After a change, run the tests of the package or files you changed (go test ./pkg/, pytest path/to/test_x.py), as then_run on the edit that completes it, not a large project's whole suite or its lint script. Report the real result.
- When the tests pass, check any rule the task states that they do not cover with a short inline assert, then stop. Do not build a test harness of your own. If your own check fails, decide once whether the code or the check is wrong.
- Rules the task states in a set order, or with "even if", "also when" or "regardless", are where hidden tests look: check each at its edge, such as an input an earlier rule decides before a later one would.
- Keep reasoning short and do not draft code in it: write code once, in the tool call.
- Paths are relative to the working directory; stay inside it. bash is non-interactive.
- Be concise: say what you changed once, at the end.`

// Two rules of Headless are rewritten when the harness takes over work the
// model was doing by hand. Measured on Kimi (effbench syn-kimi-pi), 53% of
// tool calls were the model rerunning tests (5.9 per run) or an ad-hoc
// check script (5.2 per run), and the rule above that kept checks "in an
// inline command, not in a file" made it retype the whole script as output
// tokens on every rerun. The rewrites are string replacements of the exact
// sentences, not a second const, so the two cannot drift apart; a test
// holds the off-switch prompt byte-identical to Headless.
const (
	headlessInlineRule = "put checks of your own in an inline command, not in a file."
	headlessTestRule   = "Report the real result."
	headlessAssertRule = "with a short inline assert, then stop."
)

// HeadlessWith is Headless adjusted for the harness behaviours that are on.
// autocheck says the harness reruns the model's last test command after
// each of its edits (agent.autoCheck); scratchDir, when not empty, is the
// absolute directory the model keeps one check file in (tool.OpenScratch).
// With neither it returns Headless itself.
func HeadlessWith(autocheck bool, scratchDir string) string {
	p := Headless
	if scratchDir != "" {
		p = strings.Replace(p, headlessInlineRule, "put checks of your own in one small file in "+scratchDir+" (it is outside the repository and deleted when the run ends), run it with a short command, and change it with edit_file instead of writing it again.", 1)
		p = strings.Replace(p, headlessAssertRule, "with a short assert in that file, then stop.", 1)
	}
	if autocheck {
		p = strings.Replace(p, headlessTestRule, "Run it once: the harness reruns that same command after each of your later edits and shows the result in the edit's result, so do not rerun it yourself. Report the real result.", 1)
	}
	return p
}
