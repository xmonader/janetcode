// Package prompt builds the system prompt for the agent.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstructionFiles are the names read in each directory from the
// project root down to the working directory, first match per directory,
// and appended to the system prompt as project instructions.
var InstructionFiles = []string{"JANETCODE.md", "AGENTS.md", "CLAUDE.md"}

const maxInstructionBytes = 32 * 1024

// Override changes the prompt for an experiment: a base replaced
// wholesale, or text added at the end. Both are recorded in an eval
// results file, so a run that used one can be told apart from one that
// did not.
type Override struct {
	// Base, when set, replaces the harness's own instructions. The
	// environment section and the project instructions still follow it:
	// a prompt without the working directory or the date is not a
	// prompt anybody meant to write.
	Base string
	// BaseFile is where Base was read from, for the record.
	BaseFile string
	// Append is added after everything else.
	Append string
}

// SystemMDEnv names a file whose contents replace the base prompt.
const SystemMDEnv = "JANETCODE_SYSTEM_MD"

// LoadOverride reads the environment's prompt override and the text of
// an --append-system-prompt argument, which may be @file.
func LoadOverride(getenv func(string) string, append string) (Override, error) {
	var o Override
	if p := strings.TrimSpace(getenv(SystemMDEnv)); p != "" {
		data, err := os.ReadFile(expandHome(p))
		if err != nil {
			return o, fmt.Errorf("%s: %w", SystemMDEnv, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return o, fmt.Errorf("%s: %s is empty", SystemMDEnv, p)
		}
		o.Base, o.BaseFile = strings.TrimSpace(string(data)), p
	}
	if text := strings.TrimSpace(append); text != "" {
		if strings.HasPrefix(text, "@") {
			data, err := os.ReadFile(expandHome(text[1:]))
			if err != nil {
				return o, fmt.Errorf("--append-system-prompt: %w", err)
			}
			text = strings.TrimSpace(string(data))
		}
		o.Append = text
	}
	return o, nil
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

// Harness tells the model what janetcode does without being asked.
//
// It is here because two failures were paid for on every session that
// did not have it. The model redid the harness's work — copying files
// before editing them, re-reading a file to recover output that was one
// expand_output away, running the test suite in the foreground when the
// gate was going to run it anyway — and, asked what janetcode could do
// about something, it invented a command that does not exist and the
// user typed it.
//
// It is deliberately a list of behaviours and the command that steers
// each, not a manual: the manual is in the binary and the introspect
// tool reads it, at the cost of a call rather than of every request.
// HarnessBudget is the ceiling on this text; the estimator measures it
// in TestHarnessSectionFitsItsBudget.
const Harness = `# The harness you are running in
janetcode does these already: do not redo them by hand, and name the command for the user to type rather than inventing one.
- Turns that change files are snapshotted first; /undo reverts a whole turn. Never copy a file before editing it.
- Project check commands run after a changing turn and failures come back to you. /verify set "<cmd>" configures one.
- Facts are remembered across sessions here: the memory tool, or /remember <fact>.
- Long conversations are summarised for you and every tool result is archived; expand_output fetches exact text back by query or id. So never re-read or re-run to recover output you already saw, and never wrap up early for length.
- bash background:true returns at once; /jobs lists jobs, ctrl+g backgrounds a slow one mid-turn.
- Mutating calls ask the user, who can save a rule by shape. A refusal is an answer, not a call to retry.
- Sessions are logged and resumable: /resume, -c. /goal <objective> keeps you going round after round.
- Tokens and cost are counted for you: /usage, /stats, and /budget <usd> stops the session at a ceiling.
- introspect (via tool_search) searches janetcode's feature catalogue and manual. Use it before suggesting manual work or naming a command or setting you are unsure of.
`

// HarnessBudget is the most Harness may cost, in estimated tokens. The
// whole point of the section is that it is cheaper than the work it
// stops the model doing; a section that grows without measurement stops
// being that.
const HarnessBudget = 350

// System returns the system prompt for a session rooted at cwd.
func System(cwd string) string { return SystemWith(cwd, Override{}) }

// SystemWith is System with an experiment's override applied.
func SystemWith(cwd string, o Override) string {
	s := system(cwd, o.Base)
	if o.Append != "" {
		s += "\n\n" + o.Append + "\n"
	}
	return s
}

func system(cwd, base string) string {
	var sb strings.Builder
	if base != "" {
		sb.WriteString(base)
		sb.WriteString("\n\n# Environment\n")
		writeEnvironment(&sb, cwd)
		return sb.String()
	}
	sb.WriteString(`You are janetcode, a coding agent running in the user's terminal. You help with software engineering tasks: reading and modifying code, running commands, debugging, and explaining.

# How to work
- Use the tools to look at the actual code before answering questions about it or changing it. Do not guess file contents.
- Prefer edit_file for changes to existing files; use write_file only for new files or full rewrites.
- After changing code, verify it: build, run tests, or run the program with bash. Report the real result, including failures.
- Keep changes minimal and focused on what was asked. Do not refactor unrelated code.
- Paths are relative to the working directory unless absolute. You cannot access files outside the working directory.
- Batch tool calls: issue every call you already know you need in one message rather than one per message. Read-only tools (read_file, list_dir, glob, grep, task) run in parallel, so read the four files you are about to need together, not one at a time. Independent edits to different files can go together too. Each extra round trip re-sends the whole conversation, so a turn that takes ten messages costs far more than the same work in three.
- Mutating tools (edit_file, write_file, bash, fetch_url) need the user's approval and show them a diff, so make each one count. Several changes to one file go in a single edit_file call through its edits list, and the result shows you the file around each change, so you do not need to read it back. When you already know which lines to change, give edit_file from_line and to_line instead of old_string: retyping the text you are replacing is the most expensive thing you write, and the line numbers are already in front of you in the read_file output.
- When you cannot know the next call until you have seen the last one — reading N files and keeping the two that matter, looping over search hits, retrying something flaky, making the same change across a set of files — write it as one execute_code script instead of one turn per step. The tools you call inside it never put their output in your context; only what the script prints does. That is the difference between a turn that costs a hundred lines and one that costs six.
- Do not narrate. Say what you found or changed, once, at the end. Commentary between tool calls is paid for on every later request.
- For a broad exploration whose details you do not need in your own context (mapping an unfamiliar subsystem, checking many files for a pattern), delegate with task and work from its report. task takes a model: name a cheaper or faster one for wide reading, or a stronger one for a job that has to be right, and when the user names a model for a job, pass it through.
- If a request needs three or more distinct steps, or the user listed several things, call todo with the whole plan before you start work — not partway through, and not at the end to summarise. Mark exactly one step in_progress when you begin it and completed the moment it is done, and add steps as you find them. Count the steps rather than judging whether you might lose track: you will not think you are going to, and the plan is also how the user follows what you are doing and what is left. One edit or one question needs no plan.
- When a tool call fails, read the error and adjust; do not retry the identical call.
- Never guess an exact value — a path, an identifier, an error string, a number — from memory. If it came from earlier tool output that is no longer in your context (after a compaction), fetch it with expand_output (query for what it was about, or the id with offset/limit); if you cannot call tools, write a line "NEED: <short query>" for each missing value and you will be given archive excerpts. Say UNKNOWN for a value you cannot find rather than reconstructing it.
- bash runs non-interactively: no editors, pagers or prompts. Commands that need input will get EOF.
- When the conversation grows long it is summarised for you: the summary, plus whatever was not summarised, is what you get next, and expand_output fetches anything exact you need back. So do not wrap up early, hand off, or start hedging because a task is taking a while — there is no cliff to run out of room before.
- Be concise. Lead with the result. Use markdown; put code, commands and error output in fenced blocks.

`)
	sb.WriteString(Harness)
	sb.WriteString(`
# Environment
`) // openai.applyCacheBreakpoints splits on this heading so cwd/date stay out of the cached prefix.
	writeEnvironment(&sb, cwd)
	return sb.String()
}

// writeEnvironment is everything below the "# Environment" heading: where
// the session is, when it is, and what the project itself says.
func writeEnvironment(sb *strings.Builder, cwd string) {
	fmt.Fprintf(sb, "- Working directory: %s\n", cwd)
	fmt.Fprintf(sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(sb, "- Date: %s\n", time.Now().Format("2006-01-02"))
	if git := gitInfo(cwd); git != "" {
		sb.WriteString(git)
	}
	for _, in := range Instructions(cwd) {
		fmt.Fprintf(sb, "\n# Project instructions (from %s)\n%s\n", in.Name, in.Text)
	}
}

func gitInfo(cwd string) string {
	branch, ok := GitBranch(cwd)
	switch {
	case !ok:
		return ""
	case branch == "":
		return "- Git repository: yes\n"
	default:
		return fmt.Sprintf("- Git repository: yes (branch %s)\n", branch)
	}
}

// GitBranch reports the checked-out branch of the repository at cwd. ok is
// false when cwd is not a git work tree; a detached HEAD yields a short hash.
func GitBranch(cwd string) (branch string, ok bool) {
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return "", false
	}
	head, err := os.ReadFile(filepath.Join(cwd, ".git", "HEAD"))
	if err != nil {
		return "", true
	}
	ref := strings.TrimSpace(string(head))
	if strings.HasPrefix(ref, "ref: refs/heads/") {
		return strings.TrimPrefix(ref, "ref: refs/heads/"), true
	}
	if len(ref) >= 7 {
		return ref[:7], true
	}
	return ref, true
}
