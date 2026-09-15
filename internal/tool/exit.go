package tool

import (
	"fmt"
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
func NoMatchMeaning(command string, exit int) string {
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

// exitCandidates lists the commands whose status could be the line's exit
// status: the last stage of each pipeline, for every ;/&&/||-separated
// element. Wrappers (sudo, env, timeout…) are peeled the way Classify
// peels them, so `timeout 5 grep x f` is judged as grep.
func exitCandidates(command string) []string {
	var out []string
	for _, elem := range splitLists(command) {
		stages := splitPipes(elem)
		if len(stages) == 0 {
			continue
		}
		last := strings.TrimSpace(stages[len(stages)-1])
		if last == "" {
			continue
		}
		name := leadingCommand(shell.Words(last), 0)
		if name == "" {
			return nil
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
		return fmt.Sprintf("\n[exit code 1] %s; for this command that is an answer, not a failure", m)
	}
	return fmt.Sprintf("\n[exit code %d]", exit)
}
