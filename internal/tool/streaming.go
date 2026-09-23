package tool

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// StreamingSummary describes a tool call whose input is still arriving:
// the path, once the model has written it, and how much content so far —
// so a surface can say "main.go · writing 40 lines" instead of "preparing
// write_file" for a minute. rel shortens a path for display. It returns
// "…" for a file tool whose path has not arrived yet, and "" for a tool it
// has nothing better to say about than the normal summary.
func StreamingSummary(name, partial string, rel func(string) string) string {
	path := ""
	if m := partialPathRe.FindStringSubmatch(partial); m != nil {
		path = m[1]
		if rel != nil {
			path = rel(path)
		}
	}
	switch name {
	case "write_file", "edit_file":
		if path == "" {
			return "…"
		}
		body := partial
		if i := strings.Index(partial, `"content"`); i >= 0 {
			body = partial[i:]
		} else if i := strings.Index(partial, `"new_string"`); i >= 0 {
			body = partial[i:]
		} else if i := strings.Index(partial, `"edits"`); i >= 0 {
			body = partial[i:]
		} else {
			return path
		}
		lines := strings.Count(body, `\n`) + 1
		verb := "writing"
		if name == "edit_file" {
			verb = "editing"
		}
		return fmt.Sprintf("%s · %s %d lines (%s) so far", path, verb, lines, textfmt.Bytes(int64(len(body))))
	case "bash":
		if m := partialCommandRe.FindStringSubmatch(partial); m != nil {
			return textfmt.Clip(strings.ReplaceAll(m[1], `\n`, " ⏎ "), 79) + "…"
		}
	}
	return ""
}

var (
	partialPathRe    = regexp.MustCompile(`"path"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	partialCommandRe = regexp.MustCompile(`"command"\s*:\s*"((?:[^"\\]|\\.)*)`)
)
