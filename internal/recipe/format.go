package recipe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// stepLineMax is how much of a step's compact input /recipe show and
// /recipe run --dry-run print before clipping it.
const stepLineMax = 160

// FormatSteps renders steps the way /recipe show and /recipe run
// --dry-run print them, one line per step: the tool and a compact form of
// its input. internal/tui and internal/webui both call this, so the two
// front-ends never format a step differently. An edit_file text-form step
// (old_string/new_string) shows as a compact "old → new" instead of the
// raw JSON, which is unreadable once either side has any length: what
// replay verifies and changes is exactly that pair.
func FormatSteps(steps []Step) string {
	var b strings.Builder
	for i, st := range steps {
		fmt.Fprintf(&b, "%d. %s %s\n", i+1, st.Tool, describeStep(st))
	}
	return strings.TrimRight(b.String(), "\n")
}

// describeStep is one step's compact one-line description, with the
// precondition RunRecipe checks before it (see Expect) named at the end
// when the step has one.
func describeStep(st Step) string {
	body := oldNewSummary(st)
	if body == "" {
		body = compactJSON(st.Input)
	}
	if st.Expect != nil && st.Expect.Absent {
		body += " [must not already exist with different content]"
	}
	return body
}

// oldNewSummary renders an edit_file text-form step's old_string and
// new_string as "path: old → new", each clipped, or "" for any step this
// does not apply to (line-form edits, other tools, or a malformed input
// that could not be parsed this way — compactJSON falls back for those).
func oldNewSummary(st Step) string {
	if st.Tool != "edit_file" {
		return ""
	}
	var in struct {
		Path      string `json:"path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}
	if json.Unmarshal(st.Input, &in) != nil || in.OldString == "" {
		return ""
	}
	half := stepLineMax / 2
	old := textfmt.Clip(textfmt.Squash(in.OldString), half)
	new := textfmt.Clip(textfmt.Squash(in.NewString), half)
	return fmt.Sprintf("%s: %q → %q", in.Path, old, new)
}

func compactJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return string(raw)
	}
	return textfmt.Clip(buf.String(), stepLineMax)
}

// Format renders a recipe's metadata (name, description, where it came
// from, its parameters) followed by its steps, for /recipe show.
func Format(r Recipe, steps []Step) string {
	var b strings.Builder
	b.WriteString(r.Name)
	if r.Description != "" {
		b.WriteString(" — " + r.Description)
	}
	b.WriteString("\n")
	if r.From.Session != "" {
		fmt.Fprintf(&b, "from session %s turn %d\n", r.From.Session, r.From.Turn)
	}
	if len(r.Params) > 0 {
		parts := make([]string, len(r.Params))
		for i, p := range r.Params {
			parts[i] = p.Name + "=" + p.Default
		}
		fmt.Fprintf(&b, "params: %s\n", strings.Join(parts, ", "))
	}
	b.WriteString(FormatSteps(steps))
	return strings.TrimRight(b.String(), "\n")
}

// FormatList renders every saved recipe as one line each, for /recipe
// list.
func FormatList(rs []Recipe) string {
	if len(rs) == 0 {
		return "no recipes yet: /recipe save <name> crystallises a turn's tool calls into one"
	}
	var b strings.Builder
	for _, r := range rs {
		fmt.Fprintf(&b, "%-20s %s (%s)\n", r.Name, r.Description, textfmt.Plural(len(r.Steps), "step"))
	}
	return strings.TrimRight(b.String(), "\n")
}
