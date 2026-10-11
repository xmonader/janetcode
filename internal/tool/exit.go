package tool

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/shell"
)

// Some programs use exit 1 to say "I looked and found nothing", not "I
// failed". grep with no match, diff on files that differ, test on a false
// condition: every one of those is an answer. Reported as a failure they
// cost a turn, because the model reads "[exit code 1]" as something to
// investigate and runs the command again another way.
//
// noMatchExit maps the command to what its exit 1 actually means.
var noMatchExit = map[string]string{
	"grep":      "no lines matched",
	"egrep":     "no lines matched",
	"fgrep":     "no lines matched",
	"zgrep":     "no lines matched",
	"rg":        "no lines matched",
	"ag":        "no lines matched",
	"ack":       "no lines matched",
	"diff":      "the inputs differ",
	"cmp":       "the inputs differ",
	"colordiff": "the inputs differ",
	"test":      "the condition was false",
	"[":         "the condition was false",
}

// NoMatchMeaning reports what exit code 1 means for this command line when
// it means "nothing found" rather than an error, and "" otherwise. Only
// exit 1 is special: grep's exit 2 is a real failure (an unreadable file, a
// bad pattern), and so is every other non-zero code from these programs.
// The one other exception is 141, a pipeline whose reader stopped early.
func NoMatchMeaning(command string, exit int) string {
	if exit == sigpipeExit {
		return sigpipeMeaning(command)
	}
	if exit != 1 {
		return ""
	}
	cands := exitCandidates(command)
	if len(cands) == 0 {
		return ""
	}
	meaning := ""
	for _, c := range cands {
		m, ok := noMatchExit[c]
		if !ok {
			return ""
		}
		if meaning == "" {
			meaning = m
		} else if meaning != m {
			meaning = "nothing matched"
		}
	}
	return meaning
}

// sigpipeExit is 128+SIGPIPE: under pipefail, what `git log | head -3`
// exits with once head has what it wants and git is killed for writing on.
const sigpipeExit = 141

// sigpipeMeaning excuses exit 141 when a pipeline's reader stopped early
// on purpose, which is no failure: the rest of the pipeline exited 0, or
// pipefail would have reported that stage instead. A test run is never
// excused, since the run was cut off before it could say how it ended.
func sigpipeMeaning(command string) string {
	if !strings.Contains(command, "|") || shell.IsTestRun(command) {
		return ""
	}
	for _, elem := range splitLists(command) {
		if len(splitPipes(elem)) > 1 {
			return "a later stage of the pipeline stopped reading early"
		}
	}
	return ""
}

// exitCandidates lists the commands whose status could be the line's exit
// status, one per ;/&&/||-separated element. The shell runs with
// pipefail, so a pipeline exits with its last failing stage: exit 1 from
// `grep x f | wc -l` is grep's. A pipeline's candidate is its last stage
// that answers with exit 1 (grep, diff, test), or its last stage when no
// stage does. Wrappers (sudo, env, timeout…) are peeled the way Classify
// peels them, so `timeout 5 grep x f` is judged as grep.
func exitCandidates(command string) []string {
	var out []string
	for _, elem := range splitLists(command) {
		stages := splitPipes(elem)
		if len(stages) == 0 {
			continue
		}
		name := ""
		for i := len(stages) - 1; i >= 0; i-- {
			stage := strings.TrimSpace(stages[i])
			if stage == "" {
				continue
			}
			n := leadingCommand(shell.Words(stage), 0)
			if name == "" && i == len(stages)-1 {
				name = n
				if n == "" {
					return nil
				}
			}
			if _, ok := noMatchExit[n]; ok {
				name = n
				break
			}
		}
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// leadingCommand peels wrappers and returns the program that actually runs,
// or "" when it cannot tell.
func leadingCommand(toks []string, depth int) string {
	if len(toks) == 0 || depth > shell.MaxWrapperDepth {
		return ""
	}
	skipOpts := func(i int) int {
		for i < len(toks) && strings.HasPrefix(toks[i], "-") {
			i++
		}
		return i
	}
	switch toks[0] {
	case "sudo", "doas", "nice", "nohup", "time", "command", "exec", "builtin", "stdbuf", "unbuffer":
		return leadingCommand(toks[skipOpts(1):], depth+1)
	case "env":
		i := 1
		for i < len(toks) && (strings.HasPrefix(toks[i], "-") || strings.Contains(toks[i], "=")) {
			i++
		}
		return leadingCommand(toks[i:], depth+1)
	case "timeout":
		i := skipOpts(1)
		if i < len(toks) {
			i++ // the duration
		}
		return leadingCommand(toks[i:], depth+1)
	}
	name := toks[0]
	// A leading assignment (FOO=1 grep …) is not the command.
	for strings.Contains(name, "=") && !strings.HasPrefix(name, "=") {
		toks = toks[1:]
		if len(toks) == 0 {
			return ""
		}
		name = toks[0]
	}
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// splitLists breaks a command line at ; && || and newlines, outside quotes.
func splitLists(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
		case c == ';' || c == '\n':
			out = append(out, cur.String())
			cur.Reset()
		case c == '&' && i+1 < len(s) && s[i+1] == '&':
			out = append(out, cur.String())
			cur.Reset()
			i++
		case c == '|' && i+1 < len(s) && s[i+1] == '|':
			out = append(out, cur.String())
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	kept := out[:0]
	for _, e := range out {
		if strings.TrimSpace(e) != "" {
			kept = append(kept, e)
		}
	}
	return kept
}

// splitPipes breaks one list element at single pipes, outside quotes.
func splitPipes(s string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(s):
			cur.WriteByte(c)
			cur.WriteByte(s[i+1])
			i++
		case c == '|':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	out = append(out, cur.String())
	return out
}

// exitNote is the line appended to a command's output for a non-zero exit.
func exitNote(command string, exit int) string {
	if m := NoMatchMeaning(command, exit); m != "" {
		// The "[exit code N]" shape is kept verbatim so everything that
		// reads it — placeholders, verdicts, loop detection — still does.
		if exit == sigpipeExit {
			return fmt.Sprintf("\n[exit code %d] %s; for this command that is not a failure", exit, m)
		}
		return fmt.Sprintf("\n[exit code 1] %s; for this command that is an answer, not a failure", m)
	}
	return fmt.Sprintf("\n[exit code %d]", exit)
}

// exitLineRe is the "[exit code N]" line exitNote appends, with whatever
// note follows the code on the same line.
var exitLineRe = regexp.MustCompile(`(?m)^\[exit code (\d+)\] ?(.*)$`)

// CommandFailed reads a bash result's last exit line: the code, and
// whether it is a failure. No line means the command exited 0. Exit 1
// from a search that found nothing is an answer, not a failure — the
// same reading the verdict makes, so the row and the verdict agree.
func CommandFailed(command, output string) (code int, failed bool) {
	m := exitLineRe.FindAllStringSubmatch(output, -1)
	if len(m) == 0 {
		return 0, false
	}
	code, _ = strconv.Atoi(m[len(m)-1][1])
	return code, code != 0 && NoMatchMeaning(command, code) == ""
}

// WithoutExitLine is a bash result for display: the trailing "[exit code
// N]" marker is for the model, and the row already says how the command
// ended. A note the marker carried ("no lines matched") is kept.
func WithoutExitLine(output string) string {
	t := strings.TrimRight(output, "\n")
	i := strings.LastIndex(t, "[exit code ")
	if i < 0 || (i > 0 && t[i-1] != '\n') {
		return output
	}
	m := exitLineRe.FindStringSubmatch(t[i:])
	if m == nil || len(m[0]) != len(t)-i {
		return output
	}
	note := m[2]
	if j := strings.Index(note, ";"); j >= 0 {
		note = note[:j]
	}
	rest := strings.TrimRight(t[:i], "\n")
	switch {
	case note == "":
		return rest
	case rest == "":
		return note
	}
	return rest + "\n" + note
}
