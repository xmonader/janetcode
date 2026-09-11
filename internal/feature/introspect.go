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
func Answer(c *Catalog, query string, kind Kind) string { return AnswerWidth(c, query, kind, 0) }

// AnswerWidth is Answer wrapped to a terminal of the given width, for
// /help <topic>. Zero means no wrapping, which is what the model wants:
// a request is not a terminal.
func AnswerWidth(c *Catalog, query string, kind Kind, width int) string {
	var b strings.Builder
	// A query that names a whole kind is a request for the list, not a
	// word search: "/help keys" wants the keybindings.
	if k, ok := KindNamed(query); ok && kind == "" && c != nil {
		if rows := c.OfKind(k); len(rows) > 0 {
			sub := New()
			sub.AddAll(rows)
			return sub.ListWidth(width)
		}
	}
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
	if len(hits) > maxRows {
		hits = hits[:maxRows]
	}
	if len(hits) > 0 {
		b.WriteString("janetcode features matching " + quoted(query) + ":\n")
		for _, f := range hits {
			row, when := "  "+f.Invocation()+" — "+f.What, "when: "+f.When
			if width <= 0 {
				b.WriteString(row + "\n")
				if f.When != "" {
					b.WriteString("      " + when + "\n")
				}
				continue
			}
			// Wrapped here, where the columns are known. The transcript
			// wraps to the terminal width with no hanging indent, so at
			// eighty columns the "when" line came back to column zero
			// and the rows ran into each other.
			b.WriteString(wrapAt(strings.TrimLeft(row, " "), width, 2, true) + "\n")
			if f.When != "" {
				b.WriteString(wrapAt(when, width, 6, true) + "\n")
			}
		}
	}
	// One manual section, not two: the second was never the answer, and
	// at twenty lines apiece a search for a one-line command scrolled the
	// question off the screen. The query is stripped of its filler words
	// first, or "it forgot what we did" quotes the section on /why.
	room := MaxAnswerBytes - b.Len()
	if room > 300 {
		words := contentWords(query)
		for _, s := range manual.Find(words, 3) {
			// A section is only quoted when its heading is about what
			// was asked. Ranking alone put the Hooks section under "how
			// do I stop it spending", and ten lines of somebody else's
			// JSON is worse than no second opinion. With nothing in the
			// catalogue to show, the best section is still better than
			// silence.
			if len(hits) > 0 {
				if !titleMatches(s.Title, words) {
					continue
				}
			} else if !bodyMatchesAll(s, words) {
				continue
			}
			ex := s.Excerpt(min(room-80, maxManualBytes))
			if len(ex)+b.Len() > MaxAnswerBytes {
				break
			}
			b.WriteString("\n" + ex + "\n")
			break
		}
	}
	if b.Len() == 0 {
		msg := "nothing in janetcode's catalogue or manual matches " + quoted(query) + "."
		if c != nil {
			msg += " The catalogue holds " + c.Summary() + "."
		}
		return msg + " Try a plainer word for the problem you are solving."
	}
	out := strings.TrimSpace(b.String())
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

// maxRows and maxManualBytes keep an answer to something a user reads
// rather than scrolls. Five rows is the whole of the catalogue that is
// ever relevant to one question; a manual section quoted past about
// nine hundred bytes has stopped answering it.
const (
	maxRows        = 5
	maxManualBytes = 900
)

// titleMatches reports whether a manual heading is about one of the
// words asked for, allowing the usual short inflection.
func titleMatches(title, words string) bool {
	t := strings.ToLower(title)
	for _, w := range strings.Fields(words) {
		w = strings.Trim(strings.TrimPrefix(w, "/"), ".,?!")
		if len(w) >= 3 && !weakInTitle[w] && containsWord(t, w) {
			return true
		}
	}
	return false
}

// weakInTitle are the everyday verbs that carry meaning in a feature's
// description but none in a manual heading. "my build takes forever"
// matched the section called "The terminal takes part", which is not
// what was asked. They stay out of stopWords: ctrl+g's own line is "a
// test suite is taking minutes", and that match is worth having.
var weakInTitle = map[string]bool{
	"take": true, "takes": true, "taking": true, "taken": true,
	"make": true, "makes": true, "made": true, "get": true, "gets": true,
	"got": true, "part": true, "way": true, "own": true,
}

// bodyMatchesAll is the bar a manual section has to clear when the
// catalogue found nothing: every word asked for has to be in it. The
// manual is then the only thing standing between the user and "nothing
// matched", and a section that answers half the question is not it.
func bodyMatchesAll(s manual.Section, words string) bool {
	hay := strings.ToLower(s.Title + " " + s.Body)
	n := 0
	for _, w := range strings.Fields(words) {
		w = strings.Trim(strings.TrimPrefix(w, "/"), ".,?!")
		if len(w) < 3 {
			continue
		}
		if !containsWord(hay, w) {
			return false
		}
		n++
	}
	return n > 0
}

// contentWords drops the filler from a query before it is put to the
// manual, which scores on raw word counts and will otherwise rank the
// section that happens to say "it" and "did" most often.
func contentWords(query string) string {
	fields := strings.Fields(strings.ToLower(query))
	kept := make([]string, 0, len(fields))
	for _, w := range fields {
		if !stopWords[strings.Trim(w, ".,?!")] {
			kept = append(kept, w)
		}
	}
	if len(kept) == 0 {
		return query
	}
	return strings.Join(kept, " ")
}
