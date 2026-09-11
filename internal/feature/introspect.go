package feature

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	manual "github.com/xmonader/janetcode/docs/manual"
)

// MaxAnswerBytes bounds what Introspect returns. Six hundred tokens is
// the budget; at the harness's own estimate of roughly 3.5 bytes to the
// token for prose, this is that budget with room to spare. The cap is in
// bytes because that is what the answer is built out of, and it is
// enforced by trimming whole sections rather than cutting a sentence.
const MaxAnswerBytes = 2000

// Introspect lets the model ask what the harness it is running in can
// do. It is registered deferred: its schema is not in any request until
// the model asks for it through tool_search, so the feature costs
// nothing per turn and only the sessions that need it pay for it.
//
// It answers from the catalogue and the embedded manual. There is no
// model call behind it and no network: the answer is a search over a few
// hundred structs and some tens of kilobytes of Markdown.
type Introspect struct{ Catalog *Catalog }

func (Introspect) Name() string { return "introspect" }

func (Introspect) Description() string {
	return "Search janetcode's own feature catalogue and manual: what the harness does for you automatically, which command or key does a thing, what a config key means. Use it before telling the user to do something by hand that the harness already does, and before inventing a command name. Returns at most a few hundred tokens. This is about the harness, not the project's code."
}

func (Introspect) Mutating() bool { return false }

func (Introspect) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"query":{"type":"string","description":"What you want to know, in words: \"long output\", \"undo a change\", \"run tests after edits\""},
"kind":{"type":"string","enum":["command","key","flag","tool","config","harness"],"description":"Optional: restrict to one kind of feature"}},
"required":["query"],"additionalProperties":false}`)
}

func (t Introspect) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if strings.TrimSpace(in.Query) == "" {
		return "", fmt.Errorf("introspect needs a query: what do you want to know about janetcode?")
	}
	return Answer(t.Catalog, in.Query, Kind(in.Kind)), nil
}

// Answer is the body of the introspect tool and of /help <topic>: the
// best catalogue matches, then whatever the manual has to add, trimmed
// to the budget.
func Answer(c *Catalog, query string, kind Kind) string {
	var b strings.Builder
	var hits []Feature
	if c != nil {
		hits = c.Find(query, 12)
	}
	if kind != "" {
		var keep []Feature
		for _, f := range hits {
			if f.Kind == kind {
				keep = append(keep, f)
			}
		}
		hits = keep
	}
	if len(hits) > 6 {
		hits = hits[:6]
	}
	if len(hits) > 0 {
		b.WriteString("janetcode features matching " + quoted(query) + ":\n")
		for _, f := range hits {
			fmt.Fprintf(&b, "  %-24s %s\n", f.Invocation(), f.What)
			if f.When != "" {
				fmt.Fprintf(&b, "  %-24s   when: %s\n", "", f.When)
			}
		}
	}
	room := MaxAnswerBytes - b.Len()
	if room > 300 {
		for _, s := range manual.Find(query, 2) {
			ex := s.Excerpt(room - 80)
			if len(ex)+b.Len() > MaxAnswerBytes {
				break
			}
			b.WriteString("\n" + ex + "\n")
			room = MaxAnswerBytes - b.Len()
		}
	}
	if b.Len() == 0 {
		msg := "nothing in janetcode's catalogue or manual matches " + quoted(query) + "."
		if c != nil {
			msg += " The catalogue holds " + c.Summary() + "."
		}
		return msg + " Try a plainer word for the problem you are solving."
	}
	out := strings.TrimRight(b.String(), "\n")
	if len(out) > MaxAnswerBytes {
		out = out[:MaxAnswerBytes]
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
		out += "\n… (truncated to the introspect budget)"
	}
	return out
}

func quoted(s string) string { return `"` + strings.TrimSpace(s) + `"` }
