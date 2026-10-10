// Package rollout decides when a headless run earns one more attempt at
// the same prompt, where that attempt starts, and which of the two
// attempts' trees to keep. Two opt-in mechanisms share it:
//
//   - the checkpoint-branch retry (--branch-retry): a run that ended badly
//     (stuck, a turn or tool-call ceiling, a gate still red) is branched
//     from its critical step, the step after which it stopped exploring.
//     The steps up to there are replayed from the recording at zero
//     tokens (replay.UntilCall), so the branch keeps the exploration the
//     failed run already paid for and the original, cache-warm prefix,
//     and gets one line saying what the failed attempt tried.
//   - the verdict-gated second sample (--second-sample): a run that
//     finished green on weak evidence (the verdict overclaimed, a check
//     went stale, a gaming signal) draws one independent second rollout.
//
// Everything here is a deterministic fold over the session's own events.
// Nothing calls a model and nothing is a learned score: the trajectory
// critic never beat the majority baseline, so the selector uses only facts
// the harness computed. The running of the extra attempt is
// internal/headless's (rollout.go there); this package is what it reads.
package rollout

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/session"
)

// Critical is where a failed run is branched from.
type Critical struct {
	// Step is the 1-based model call after which the run stopped
	// exploring: a branch replays the recorded responses 1..Step and goes
	// live from call Step+1. Zero means there is no exploration to keep.
	Step int `json:"step"`
	// Calls is how many model calls the recording holds in all.
	Calls int `json:"calls"`
	// FirstEdit is the 1-based model call whose tool calls first changed
	// the tree; zero when the run never changed anything.
	FirstEdit int `json:"first_edit,omitempty"`
	// Explored is how many distinct things (files read, directories
	// listed, searches run) the run had looked at by Step.
	Explored int `json:"explored"`
}

// OK reports whether there is a branch point worth starting from: some
// exploration to keep, and at least one model call after it to redo.
func (c Critical) OK() bool { return c.Step > 0 && c.Step < c.Calls }

// Share is the fraction of the run's model calls a branch replays rather
// than buys again.
func (c Critical) Share() float64 {
	if c.Calls == 0 {
		return 0
	}
	return float64(c.Step) / float64(c.Calls)
}

// CriticalStep picks a failed run's branch point with SWE-Replay's
// exploration rule: the last model call that grew the set of things the
// run had looked at, among the calls before its first edit (all of its
// calls when it never edited). Exploration is reading — read_file,
// list_dir, grep, glob, and bash commands that only read (cat, ls, grep,
// find, sed -n, ...) — and a call grows it when it looks at something
// no earlier call had. Running the tests is not exploration: it reads the
// code's behaviour, not the repository, and its answer is what the branch
// will want to see again anyway.
//
// The rule beat an LLM-judge selector in SWE-Replay (60% against 54%), and
// it is a fold, not a judgement, so the same recording always branches at
// the same step. A run that edited before looking at anything (the whole
// repository was inlined in its prompt, say) has Step zero: there is no
// paid-for exploration to keep, and a branch would be a rerun from
// scratch.
func CriticalStep(events []session.Event) Critical {
	var c Critical
	// First pass: the model calls, and the first one that changed the
	// tree — by an edit tool, or by a bash command whose write the log
	// recorded as a file change.
	step := map[string]int{} // call id -> the model call that made it
	for _, e := range events {
		switch e.Type {
		case session.TypeAssistant:
			if e.Message != nil && e.SurfaceOp != session.SurfaceOpReplace {
				c.Calls++
			}
		case session.TypeToolCall:
			if session.IsNestedCall(e.CallID) {
				continue
			}
			step[e.CallID] = c.Calls
			if agent.MutatingTool(e.Name) && c.FirstEdit == 0 {
				c.FirstEdit = c.Calls
			}
		case session.TypeFileChange:
			if artefact(e.Name) {
				continue
			}
			if s, ok := step[e.CallID]; ok && s > 0 && (c.FirstEdit == 0 || s < c.FirstEdit) {
				c.FirstEdit = s
			}
		}
	}
	// Second pass: the last call before the first edit that looked at
	// something new.
	seen := map[string]bool{}
	for _, e := range events {
		if e.Type != session.TypeToolCall || session.IsNestedCall(e.CallID) {
			continue
		}
		s := step[e.CallID]
		if c.FirstEdit != 0 && s >= c.FirstEdit {
			continue
		}
		for _, k := range exploreKeys(e.Name, e.Arguments) {
			if !seen[k] {
				seen[k] = true
				c.Step = s
			}
		}
	}
	c.Explored = len(seen)
	return c
}

// artefact reports a changed path that is a by-product of running
// something rather than an edit: a test run that compiles bytecode or
// fills a cache has not changed the code, and counting it would make a
// baseline test run the run's first edit.
func artefact(path string) bool {
	p := "/" + filepath.ToSlash(path)
	for _, dir := range []string{"__pycache__", ".pytest_cache", "node_modules", "target", ".mypy_cache", ".ruff_cache", ".git"} {
		if strings.Contains(p, "/"+dir+"/") {
			return true
		}
	}
	return strings.HasSuffix(p, ".pyc")
}

// exploreKeys is what one call looked at, one key per thing, empty for a
// call that is not exploration.
func exploreKeys(name, arguments string) []string {
	var in map[string]any
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return nil
	}
	str := func(k string) string { v, _ := in[k].(string); return strings.TrimSpace(v) }
	switch name {
	case "read_file", "list_dir":
		if p := str("path"); p != "" {
			return []string{"file\x00" + cleanPath(p)}
		}
	case "read_many_files":
		var out []string
		if ps, ok := in["paths"].([]any); ok {
			for _, p := range ps {
				if s, ok := p.(string); ok && s != "" {
					out = append(out, "file\x00"+cleanPath(s))
				}
			}
		}
		return out
	case "grep", "glob":
		// A search is new when its question is: the same pattern over
		// the same path learns nothing the first one did not.
		return []string{name + "\x00" + str("pattern") + "\x00" + cleanPath(str("path"))}
	case "bash":
		return bashReads(str("command"))
	}
	return nil
}

// readers are the commands that only look: a bash call made of nothing
// else is exploration, and its non-flag words are what it looked at.
var readers = map[string]bool{
	"cat": true, "head": true, "tail": true, "ls": true, "find": true, "grep": true,
	"rg": true, "wc": true, "tree": true, "nl": true, "file": true, "stat": true,
	"less": true, "more": true, "sed": true, "awk": true,
}

// bashReads splits a command on its separators and returns a key per
// target of every segment that only reads. A segment that does anything
// else (runs a test, writes, installs) contributes nothing; a cd is
// skipped as plumbing. sed counts as a reader only with -n and no -i,
// the form models use to print a line range.
func bashReads(cmd string) []string {
	var out []string
	r := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	for _, seg := range strings.Split(r.Replace(cmd), "\n") {
		words := strings.Fields(seg)
		if len(words) == 0 || words[0] == "cd" {
			continue
		}
		head := filepath.Base(words[0])
		if !readers[head] {
			continue
		}
		if head == "sed" && (!hasWord(words, "-n") || hasPrefixWord(words, "-i")) {
			continue
		}
		if hasPrefixWord(words, ">") {
			continue
		}
		var args []string
		for _, w := range words[1:] {
			if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "<>") {
				continue
			}
			args = append(args, strings.Trim(w, `"'`))
		}
		switch head {
		case "grep", "rg":
			// A search is one question, the pattern with where it looked,
			// keyed like the grep tool's so the two count as one.
			if len(args) > 0 {
				out = append(out, "grep\x00"+args[0]+"\x00"+cleanPath(strings.Join(args[1:], " ")))
			}
		case "ls", "find", "tree":
			if len(args) == 0 {
				args = []string{"."}
			}
			for _, a := range args {
				out = append(out, "file\x00"+cleanPath(a))
			}
		default:
			// A reader with no file argument reads its stdin, the output
			// of the segment before it, which looked at nothing new.
			for _, a := range args {
				if head == "sed" && !strings.ContainsAny(a, "./") {
					continue // the line range, not a file
				}
				out = append(out, "file\x00"+cleanPath(a))
			}
		}
	}
	return out
}

func hasWord(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

func hasPrefixWord(words []string, p string) bool {
	for _, x := range words {
		if strings.HasPrefix(x, p) {
			return true
		}
	}
	return false
}

func cleanPath(p string) string {
	if p == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(p))
}
