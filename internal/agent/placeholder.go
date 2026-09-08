package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// prunedPrefix marks a tool result that was replaced by its placeholder.
const prunedPrefix = "[pruned: "

var reExitCode = regexp.MustCompile(`\[exit code (\d+)\]`)

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
		if m := reExitCode.FindStringSubmatch(body); m != nil {
			exit = m[1]
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
			out += "; last line: " + trunc(last, 120)
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
		out = name + ": " + trunc(first, 120)
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

func quoteTrunc(s string, n int) string { return fmt.Sprintf("%q", trunc(s, n)) }

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}
