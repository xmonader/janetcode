package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

// ExpandOutput retrieves archived tool output: a slice of one entry by id,
// lines matching a pattern in one or all entries, or the list of entries.
// Its own output is never archived, only capped.
type ExpandOutput struct {
	Archive *Archive
}

const expandDefaultLimit = 200

func (*ExpandOutput) Name() string { return "expand_output" }
func (*ExpandOutput) Description() string {
	return "Read archived output: tool results that were too long to show inline, and the text of earlier conversation that was compacted away (ids like ctx-12; results and compaction notes say which). Give an id with offset/limit to read a slice of lines; grep (a regexp) for matching lines with line numbers; or query (plain words: what you are looking for, e.g. a path, an error message, a value) to get the best-matching passages ranked — use query when you do not know the exact wording. Without an id, grep and query search every archived output. With no arguments it lists what is archived."
}
func (*ExpandOutput) Mutating() bool { return false }
func (*ExpandOutput) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"id":{"type":"string","description":"Archive id from a truncated result, e.g. ctx-12"},
"grep":{"type":"string","description":"Regexp; returns matching lines with line numbers"},
"query":{"type":"string","description":"Words describing what to find; returns the best-matching passages ranked, any word may match"},
"offset":{"type":"integer","description":"First line to return (1-based, default 1)"},
"limit":{"type":"integer","description":"Lines to return (default 200)"}},
"additionalProperties":false}`)
}

func (t *ExpandOutput) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		ID     string `json:"id"`
		Grep   string `json:"grep"`
		Query  string `json:"query"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if t.Archive == nil {
		return "", fmt.Errorf("no output archive is configured in this session")
	}
	var re *regexp.Regexp
	if in.Grep != "" {
		var err error
		if re, err = regexp.Compile("(?i)" + in.Grep); err != nil {
			return "", fmt.Errorf("bad grep pattern: %w", err)
		}
	}
	if strings.TrimSpace(in.Query) != "" {
		var ids []string
		if in.ID != "" {
			ids = []string{in.ID}
		}
		return searchArchive(t.Archive, ids, in.Query, 0), nil
	}
	switch {
	case in.ID == "" && re == nil:
		return t.list(), nil
	case in.ID == "":
		return t.grepAll(re), nil
	}
	e, body, err := t.Archive.Get(in.ID)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if re != nil {
		out := grepLines(lines, re, in.Limit)
		if out == "" {
			return fmt.Sprintf("%s (%s, %d lines): no line matches %q", e.ID, e.Name, len(lines), in.Grep), nil
		}
		return tool.Truncate(fmt.Sprintf("%s (%s, %d lines), lines matching %q:\n%s", e.ID, e.Name, len(lines), in.Grep, out)), nil
	}
	if in.Offset < 1 {
		in.Offset = 1
	}
	if in.Limit <= 0 {
		in.Limit = expandDefaultLimit
	}
	if in.Offset > len(lines) {
		return "", fmt.Errorf("%s has only %d lines", e.ID, len(lines))
	}
	end := in.Offset - 1 + in.Limit
	if end > len(lines) {
		end = len(lines)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%s, %d lines), lines %d-%d:\n", e.ID, e.Name, len(lines), in.Offset, end)
	for i := in.Offset - 1; i < end; i++ {
		fmt.Fprintf(&sb, "%d\t%s\n", i+1, lines[i])
	}
	if end < len(lines) {
		fmt.Fprintf(&sb, "[%d more lines; continue with offset %d]\n", len(lines)-end, end+1)
	}
	return tool.Truncate(sb.String()), nil
}

func (t *ExpandOutput) list() string {
	es := t.Archive.List()
	if len(es) == 0 {
		return "Nothing is archived yet."
	}
	var sb strings.Builder
	sb.WriteString("Archived outputs, newest first:\n")
	for _, e := range es {
		fmt.Fprintf(&sb, "%s\t%s\t%s\t%s\n", e.ID, e.Name, humanBytes(e.Size), e.Created.Local().Format("15:04:05"))
	}
	return sb.String()
}

func (t *ExpandOutput) grepAll(re *regexp.Regexp) string {
	var sb strings.Builder
	for _, e := range t.Archive.List() {
		_, body, err := t.Archive.Get(e.ID)
		if err != nil {
			continue
		}
		if out := grepLines(strings.Split(strings.TrimSuffix(body, "\n"), "\n"), re, 20); out != "" {
			fmt.Fprintf(&sb, "%s (%s):\n%s\n", e.ID, e.Name, out)
		}
		if sb.Len() > tool.MaxOutput {
			break
		}
	}
	if sb.Len() == 0 {
		return fmt.Sprintf("No archived output has a line matching %q.", re.String())
	}
	return tool.Truncate(sb.String())
}

func grepLines(lines []string, re *regexp.Regexp, limit int) string {
	if limit <= 0 {
		limit = expandDefaultLimit
	}
	var sb strings.Builder
	n := 0
	for i, l := range lines {
		if !re.MatchString(l) {
			continue
		}
		n++
		if n > limit {
			fmt.Fprintf(&sb, "[more matches not shown; narrow the pattern or raise limit]\n")
			break
		}
		fmt.Fprintf(&sb, "%d\t%s\n", i+1, l)
	}
	return sb.String()
}
