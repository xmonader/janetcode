package llm

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Some models — small ones, quantised ones, anything served through a
// gateway that dropped the tool schema — write the call they meant to
// make as prose instead of emitting it on the wire: an XML <invoke> block
// or a <tool_call> JSON object in the text. The turn then ends with no
// tool call and nothing done, and the user pays for a round trip that
// produced a description of an action. Reading those shapes back into
// real calls costs nothing when they do not appear, and rescues the turn
// when they do.

var (
	invokeRe    = regexp.MustCompile(`(?s)<invoke\s+name=["']([^"']+)["']\s*>(.*?)</invoke>`)
	parameterRe = regexp.MustCompile(`(?s)<parameter\s+name=["']([^"']+)["']\s*>(.*?)</parameter>`)
	toolCallRe  = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	fenceRe     = regexp.MustCompile("^ {0,3}(```+|~~~+)")
)

// RecoveredCall is a tool call read back out of assistant text.
type RecoveredCall struct {
	Name  string
	Input json.RawMessage
}

// RecoverToolCalls finds tool calls written as prose. It returns the
// calls in the order they appeared and the text with those blocks
// removed. ok is false when there is nothing to recover, and when the
// blocks are a small part of a mostly prose answer — a model explaining
// the format, or quoting one back, is not asking for the call.
func RecoverToolCalls(text string) (calls []RecoveredCall, remaining string, ok bool) {
	if text == "" || (!strings.Contains(text, "<invoke") && !strings.Contains(text, "<tool_call>")) {
		return nil, text, false
	}
	type span struct {
		start, end int
		call       RecoveredCall
	}
	var spans []span
	for _, m := range invokeRe.FindAllStringSubmatchIndex(text, -1) {
		if insideFence(text, m[0]) {
			continue
		}
		name := text[m[2]:m[3]]
		body := text[m[4]:m[5]]
		args := map[string]any{}
		for _, p := range parameterRe.FindAllStringSubmatch(body, -1) {
			args[p[1]] = parameterValue(p[2])
		}
		if name == "" || len(args) == 0 {
			// A parameterless block says nothing a call could be made
			// from; leave it in the text as the prose it is.
			continue
		}
		raw, err := json.Marshal(args)
		if err != nil {
			continue
		}
		spans = append(spans, span{m[0], m[1], RecoveredCall{Name: name, Input: raw}})
	}
	for _, m := range toolCallRe.FindAllStringSubmatchIndex(text, -1) {
		if insideFence(text, m[0]) {
			continue
		}
		var obj struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Params    json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal([]byte(text[m[2]:m[3]]), &obj) != nil || obj.Name == "" {
			continue
		}
		args := obj.Arguments
		if len(args) == 0 {
			args = obj.Params
		}
		// Some servers double-encode the arguments as a JSON string.
		if len(args) > 0 && args[0] == '"' {
			var s string
			if json.Unmarshal(args, &s) == nil && json.Valid([]byte(s)) {
				args = json.RawMessage(s)
			}
		}
		if len(args) == 0 || !json.Valid(args) {
			args = json.RawMessage(`{}`)
		}
		spans = append(spans, span{m[0], m[1], RecoveredCall{Name: obj.Name, Input: args}})
	}
	if len(spans) == 0 {
		return nil, text, false
	}
	// In source order, so the calls run in the order the model wrote them.
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
	var b strings.Builder
	last, blockLen := 0, 0
	for _, s := range spans {
		if s.start < last {
			continue // overlapping shapes: keep the first
		}
		b.WriteString(text[last:s.start])
		blockLen += s.end - s.start
		last = s.end
		calls = append(calls, s.call)
	}
	b.WriteString(text[last:])
	// A block that is a footnote to a page of prose is a description of
	// a call, not a call.
	if blockLen*5 < len(text) {
		return nil, text, false
	}
	remaining = strings.TrimSpace(collapse(b.String()))
	remaining = strings.TrimSpace(strings.NewReplacer("<function_calls>", "", "</function_calls>", "", "<function_results>", "", "</function_results>", "").Replace(remaining))
	return calls, remaining, true
}

// parameterValue reads one <parameter> body: the dialect puts a newline
// after the open tag and before the close tag, and everything else is
// significant (the indentation that makes an edit's old_string unique).
func parameterValue(v string) any {
	v = strings.TrimPrefix(v, "\n")
	v = strings.TrimSuffix(v, "\n")
	v = xmlEntities.Replace(v)
	trimmed := strings.TrimSpace(v)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var any any
		if json.Unmarshal([]byte(trimmed), &any) == nil {
			return any
		}
	}
	// A scalar stays a string: a file named "null" and a port "8080" are
	// not what JSON would make of them.
	return v
}

// &amp; is decoded last, so &amp;lt; ends up as &lt; and not as <.
var xmlEntities = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")

var blankRuns = regexp.MustCompile(`\n{3,}`)

func collapse(s string) string { return blankRuns.ReplaceAllString(s, "\n\n") }

// insideFence reports whether an offset falls inside an open code fence,
// where a block is documentation rather than a request.
func insideFence(text string, at int) bool {
	open := ""
	for _, line := range strings.Split(text[:at], "\n") {
		m := fenceRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch {
		case open == "":
			open = m[1]
		case m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(line[len(m[0]):]) == "":
			open = ""
		}
	}
	return open != ""
}

// RecoveredBlocks turns recovered calls into tool_use blocks with ids
// that cannot collide with a provider's own.
func RecoveredBlocks(calls []RecoveredCall, prefix string) []Block {
	out := make([]Block, 0, len(calls))
	for i, c := range calls {
		out = append(out, Block{Type: BlockToolUse, ID: fmt.Sprintf("%s_%d", prefix, i+1), Name: c.Name, Input: c.Input})
	}
	return out
}
