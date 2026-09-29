// Package present decides what the front-ends show for the agent's work:
// the words and the facts, not how they are drawn. The terminal and the
// browser each made these decisions on their own and drifted — the same
// tool call read one way in the TUI and another in the web page, and one
// of them printed an MCP call's arguments that the other hid. Both call
// here now, so a row says the same thing wherever it is read.
package present

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// ToolSummary is a tool call on one line of at most max columns: the
// argument that says what the call does, with paths relative to cwd.
// The model often passes paths absolute, and an absolute path clipped to
// fit loses the one part that matters, the file name.
func ToolSummary(name, raw string, max int, cwd string) string {
	// MCP tools are third-party code, and their argument values often
	// carry credentials the model copied from context — an api_key, a
	// bearer token. The line lands in scrollback, screenshots and the
	// browser's event stream, so it names the argument keys only.
	if strings.HasPrefix(name, "mcp__") {
		return Clip(argumentKeys(raw), max)
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			return Clip(raw, max)
		}
		// Still streaming: the arguments are half a JSON object. Showing
		// them raw put `{"path": "/tmp/very/long/abs` on screen; the
		// value that names the call is read out of the prefix instead,
		// once it has arrived whole, and nothing is shown before.
		k, v, ok := partialArgument(raw)
		if !ok {
			return ""
		}
		full, _ := json.Marshal(map[string]string{k: v})
		return ToolSummary(name, string(full), max, cwd)
	}
	// The plan tool's argument is the whole list: say how many steps, and
	// the one being worked on.
	if todos, ok := m["todos"].([]any); ok {
		sum := textfmt.Plural(len(todos), "step")
		for _, t := range todos {
			if item, ok := t.(map[string]any); ok && item["status"] == "in_progress" {
				if text, ok := item["text"].(string); ok && text != "" {
					sum += " · " + text
				}
				break
			}
		}
		return Clip(sum, max)
	}
	for _, k := range []string{"command", "pattern", "path", "url", "description"} {
		v, ok := m[k].(string)
		if !ok {
			continue
		}
		extra := ""
		switch k {
		case "command", "description":
			// A command redirecting into the project carried the whole
			// absolute path, the same for every call, and it ate the
			// width the command itself needed.
			if k == "command" {
				v = shell.TrimWorkspaceCd(v, cwd)
			}
			v = ShortenPaths(cwd, v)
		case "pattern":
			if p, ok := m["path"].(string); ok && p != "" {
				extra = " in " + RelPath(cwd, p)
			}
		case "path":
			v = RelPath(cwd, v)
			if off, ok := m["offset"].(float64); ok && off > 1 {
				extra = fmt.Sprintf(" from line %d", int(off))
			}
		}
		return Clip(v+extra, max)
	}
	return Clip(raw, max)
}

// argumentKeys renders arguments as their key names alone, sorted so the
// line is stable, never the values. A body that is not a JSON object has
// no keys to name, so it is described by its shape instead of quoted.
func argumentKeys(raw string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "(arguments are not valid JSON)"
	}
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return "{" + strings.Join(keys, ", ") + "}"
	case []any:
		return "(json array)"
	case string:
		return "(json string)"
	}
	return "(json value)"
}

// Clip fits s on one line of at most max columns: newlines show as ⏎,
// wide runes count double and are never split, and a cut ends in "…".
// Below 10 columns nothing useful fits, so 10 is the floor.
func Clip(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", "⏎ ")
	if max < 10 {
		max = 10
	}
	return ansi.Truncate(s, max, "…")
}

// RelPath shows p relative to cwd when it lies under it, and with the
// home directory as ~ otherwise. A relative path is returned as it is.
func RelPath(cwd, p string) string {
	if cwd == "" || !filepath.IsAbs(p) {
		return p
	}
	if r := textfmt.RelTo(cwd, p); r != p {
		return r
	}
	// Home itself or a path under it — not any path that merely starts
	// with the same letters, which turned /home/me2/x into "~2/x".
	if home, err := os.UserHomeDir(); err == nil && home != "" && (p == home || strings.HasPrefix(p, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// ShortenPaths takes the project directory out of the paths inside a
// command and writes the home directory as ~.
func ShortenPaths(cwd, s string) string {
	if cwd != "" {
		s = strings.ReplaceAll(s, cwd+string(filepath.Separator), "")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home+string(filepath.Separator), "~"+string(filepath.Separator))
	}
	return s
}

// partialArg finds the first of the summarised keys whose string value
// has arrived whole in a JSON prefix, e.g. `{"path": "/tmp/a.go", "con`.
// A value still arriving is not shown: half an absolute path is the
// part of it that says nothing.
var partialArg = regexp.MustCompile(`"(command|pattern|path|url|description)"\s*:\s*("(?:[^"\\]|\\.)*")`)

func partialArgument(raw string) (key, value string, ok bool) {
	m := partialArg.FindStringSubmatch(raw)
	if m == nil {
		return "", "", false
	}
	if json.Unmarshal([]byte(m[2]), &value) != nil || value == "" {
		return "", "", false
	}
	return m[1], value, true
}
