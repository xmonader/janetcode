package tool

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// How a tool call reads in the transcript used to be a switch on the
// tool's name in the UI: read_file folded to a summary, edit_file had its
// numbered window stripped, todo printed only what changed, bash kept its
// tail. Every one of those was knowledge about a tool living somewhere
// the tool could not reach — and an MCP or user-supplied tool could never
// have any of it.
//
// A tool may now say for itself how its call and its result should read.
// Returning nil means "nothing special", and the UI falls back to what it
// does for every other tool, which is what MCP tools get.
type Renderer interface {
	// RenderCall summarises the call itself in one line, or nil.
	RenderCall(input json.RawMessage, o RenderContext) []string
	// RenderResult presents the result as lines, or nil.
	RenderResult(input json.RawMessage, out string, o RenderContext) []string
}

// RenderContext is what the UI can tell a tool about the space it has.
type RenderContext struct {
	// Expanded is set when the reader asked for this entry in full.
	Expanded bool
	// Max is how many lines may be drawn before folding.
	Max int
	// Prev is this tool's previous result in the same conversation, for a
	// tool whose output is worth showing as a change rather than whole.
	Prev string
}

// HeadTail keeps the first head and the last tail lines of s with a count
// of what was dropped between them. The head alone is the wrong half of a
// build log: the error is at the end.
func HeadTail(s string, head, tail int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) < head+tail+4 {
		return lines
	}
	out := append([]string{}, lines[:head]...)
	out = append(out, fmt.Sprintf("… +%d lines", len(lines)-head-tail))
	return append(out, lines[len(lines)-tail:]...)
}

// --- bash ---

func (Bash) RenderCall(json.RawMessage, RenderContext) []string { return nil }

// RenderResult keeps the tail of the output: what a command has to say
// about how it went is on its last lines.
func (Bash) RenderResult(_ json.RawMessage, out string, o RenderContext) []string {
	if o.Expanded {
		return nil
	}
	return HeadTail(out, 2, 3)
}

// --- read_file ---

func (ReadFile) RenderCall(json.RawMessage, RenderContext) []string { return nil }

// RenderResult folds a read to one line. The numbered text is for the
// model; what the reader needs is which file was read and how big it is.
// Expanded, it is a sample of the file rather than the whole window.
func (ReadFile) RenderResult(input json.RawMessage, out string, o RenderContext) []string {
	if o.Expanded {
		return HeadTail(out, 5, 3)
	}
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
	}
	json.Unmarshal(input, &in)
	label := in.Path
	if label != "" && in.Offset > 1 {
		label += fmt.Sprintf(" from line %d", in.Offset)
	}
	if s := ReadSummary(out, label); s != "" {
		return []string{s}
	}
	return nil
}

// numberedLine matches one line of a read_file result or of the numbered
// window edit_file appends: "  12\ttext", or "12:a4f\ttext" with anchors.
var numberedLine = regexp.MustCompile(`^\s*\d+(:[0-9a-z]+)?\t`)

var moreLines = regexp.MustCompile(`^\.\.\. \((\d+) more lines\)$`)

// ReadSummary folds a windowed read to one line, or returns "" for
// anything that is not one.
func ReadSummary(body, label string) string {
	shown, more := 0, 0
	for _, l := range strings.Split(body, "\n") {
		switch {
		case numberedLine.MatchString(l):
			shown++
		case moreLines.MatchString(l):
			more, _ = strconv.Atoi(moreLines.FindStringSubmatch(l)[1])
		case strings.TrimSpace(l) == "":
		default:
			return ""
		}
	}
	if shown == 0 {
		return ""
	}
	if label == "" {
		label = "file"
	}
	if more > 0 {
		return fmt.Sprintf("read %s: %d of %d lines", label, shown, shown+more)
	}
	if shown == 1 {
		return fmt.Sprintf("read %s: 1 line", label)
	}
	return fmt.Sprintf("read %s: %d lines", label, shown)
}

// --- edit_file ---

func (EditFile) RenderCall(json.RawMessage, RenderContext) []string { return nil }

// RenderResult drops the numbered window the edit appends to its result.
// The model still gets it; the transcript has already shown the change as
// a diff just above.
func (EditFile) RenderResult(_ json.RawMessage, out string, o RenderContext) []string {
	if o.Expanded {
		return nil
	}
	s := StripEditWindow(out)
	if s == out {
		return nil
	}
	return strings.Split(s, "\n")
}

// StripEditWindow removes the numbered window at the end of an edit's
// result, when there is one.
func StripEditWindow(out string) string {
	i := strings.LastIndex(out, "\n\n")
	if i < 0 {
		return out
	}
	tail := out[i+2:]
	if strings.HasPrefix(tail, "(the change spans lines ") {
		return out[:i]
	}
	for _, l := range strings.Split(tail, "\n") {
		if !numberedLine.MatchString(l) {
			return out
		}
	}
	return out[:i]
}
