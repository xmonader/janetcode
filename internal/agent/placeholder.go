package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// prunedPrefix marks a tool result that was replaced by its placeholder.
const prunedPrefix = "[pruned: "

var reExitCode = regexp.MustCompile(`\[exit code (\d+)\]`)

// lastExitCode is the code of the last "[exit code N]" in a bash result,
// which is the one the tool appended after the output: the command's own
// output can carry the same shape (a saved transcript, a test printing
// one), and the first of them is then not the command's.
func lastExitCode(text string) (string, bool) {
	ms := reExitCode.FindAllStringSubmatch(text, -1)
	if len(ms) == 0 {
		return "", false
	}
	return ms[len(ms)-1][1], true
}

// placeholder condenses an old tool result to one line that still says
// what the call was and how it went, so the model can decide whether the
// full output (kept in the archive) is worth fetching.
func placeholder(name string, input json.RawMessage, text, archiveID string) string {
	var in map[string]any
	json.Unmarshal(input, &in)
	str := func(k string) string {
		v, _ := in[k].(string)
		return strings.TrimSpace(v)
	}
	body := strings.TrimSpace(text)
	if i := strings.Index(body, "\n"); strings.HasPrefix(body, "[output is ") && i > 0 {
		body = strings.TrimSpace(body[i+1:]) // drop an earlier archive note
	}
	lines := nonEmptyLines(body)
	var out string
	switch name {
	case "bash":
		exit := "0"
		if code, ok := lastExitCode(body); ok {
			exit = code
		}
		last := ""
		for i := len(lines) - 1; i >= 0; i-- {
			if !reExitCode.MatchString(lines[i]) && !strings.HasPrefix(lines[i], "[") {
				last = lines[i]
				break
			}
		}
		out = fmt.Sprintf("bash %s → exit %s", quoteTrunc(str("command"), 80), exit)
		if last != "" {
			out += "; last line: " + textfmt.Clip(strings.TrimSpace(last), 120)
		}
	case "read_file":
		out = fmt.Sprintf("read_file %s: %d lines were shown", str("path"), len(lines))
	case "grep":
		out = fmt.Sprintf("grep %s: %d matching lines", quoteTrunc(str("pattern"), 60), len(lines))
	case "glob":
		out = fmt.Sprintf("glob %s: %d entries", quoteTrunc(str("pattern"), 60), len(lines))
	case "list_dir":
		out = fmt.Sprintf("list_dir %s: %d entries", orDot(str("path")), len(lines))
	default:
		first := ""
		if len(lines) > 0 {
			first = lines[0]
		}
		out = name + ": " + textfmt.Clip(strings.TrimSpace(first), 120)
	}
	if archiveID != "" {
		out += fmt.Sprintf(` (full output: expand_output {"id":%q})`, archiveID)
	}
	return prunedPrefix + out + "]"
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func quoteTrunc(s string, n int) string {
	return fmt.Sprintf("%q", textfmt.Clip(strings.TrimSpace(s), n))
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}
