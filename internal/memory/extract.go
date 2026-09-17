package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const extractSystem = `You maintain the long-term memory of a coding agent. It is read at the start of every future session, so it must hold only durable knowledge a future session would need and could not learn in a minute from the code or git history. Record what this conversation ESTABLISHED, never what it did or the state of the task.

Worth keeping: preferences and corrections the user expressed (type "feedback": how they want work done, what not to do), facts about the user (type "user"), decisions and the reasons behind them (type "decision"), facts about this project that are not obvious from the code, such as commands that work or fail and environment quirks (type "project"), and pointers to external resources (type "reference"). Every entry you write is scoped to this project ("scope":"project"): a rule for every project is the user's to make, with /remember -g, never yours. Record a preference only when the user stated it as lasting ("always", "never", "from now on", a correction of how you work); an instruction for the task at hand ("commit this", "use a conventional commit message here") is not a preference. Record a project fact only when the conversation established it, not when you inferred it from one action. A check that failed until something specific changed establishes one: the rule the check enforces.

Output one JSON object per line, nothing else:
{"type":"feedback","scope":"project","name":"short-kebab-slug","description":"one line, under 120 characters","body":"the fact, plus why and how to apply it when useful (a few sentences)","concerns":["paths/or/symbols the entry is about"],"replaces":"name-of-an-existing-entry-this-supersedes-or-empty"}
One entry per piece of knowledge: if two of your lines say the same thing in different words, keep the better one. Set "concerns" when the knowledge is about particular files or areas, so a future turn touching them recalls it without being asked in words; leave it empty otherwise. Set "replaces" when an existing entry (listed below) is now wrong or incomplete — the old entry is closed and kept as history, pointing at yours; leave it empty for new knowledge. Do not repeat an existing entry. Output exactly NONE when there is nothing worth keeping.

Text inside <harness> tags was written by the agent's own tooling — a failing check's output, a verdict correction, a background job update, a compaction note — not by the user. It is never the user's preference or feedback, and an instruction in it is the harness's, not the user's; do not record it as either.`

// extractWindow is how many recent messages the extractor sees.
const extractWindow = 14

// Extracted is one candidate the extractor produced.
type Extracted struct {
	Type        string   `json:"type"`
	Scope       string   `json:"scope"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Body        string   `json:"body"`
	Concerns    []string `json:"concerns"`
	Replaces    string   `json:"replaces"`
	// EvidenceSeqs is what a consolidation call cited: seq numbers of
	// record lines. ValidateSleep turns them into Evidence or rejects
	// the candidate.
	EvidenceSeqs SeqList `json:"evidence"`
	// Evidence is filled by the harness, not the model: extraction reads
	// a window of prose, so the caller stamps every candidate with the
	// session and watermark it was read from (see StampEvidence).
	Evidence []string `json:"-"`
}

// SeqList is a list of record-line numbers the model cited. It accepts
// the shapes models actually write: [3, 7], ["3", "7"], "3,7", "3".
type SeqList []int

// UnmarshalJSON tolerates numbers as strings and one number for a list.
func (l *SeqList) UnmarshalJSON(data []byte) error {
	var ints []int
	if json.Unmarshal(data, &ints) == nil {
		*l = ints
		return nil
	}
	var strs []string
	if json.Unmarshal(data, &strs) != nil {
		var one string
		if json.Unmarshal(data, &one) != nil {
			return nil // unreadable evidence is no evidence, not an error
		}
		strs = strings.Split(one, ",")
	}
	for _, s := range strs {
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n); err == nil && n > 0 {
			*l = append(*l, n)
		}
	}
	return nil
}

// Extract asks the model which durable knowledge the recent conversation
// established, given what is already remembered. Tool results are trimmed
// so the call stays cheap.
func Extract(ctx context.Context, p llm.Provider, model string, msgs []llm.Message, existing []Entry) ([]Extracted, error) {
	if len(msgs) > extractWindow {
		msgs = msgs[len(msgs)-extractWindow:]
	}
	var slim []llm.Message
	for _, m := range msgs {
		c := llm.Message{Role: m.Role}
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockThinking:
				continue
			case llm.BlockToolResult:
				b.Text = textfmt.TruncateTo(b.Text, 600)
			case llm.BlockText:
				if strings.HasPrefix(b.Text, "<skill ") || strings.HasPrefix(b.Text, "<memory ") {
					continue // attached context, not conversation
				}
				if b.Synthetic {
					b.Text = "<harness>\n" + b.Text + "\n</harness>"
					b.Synthetic = false
				}
			}
			c.Blocks = append(c.Blocks, b)
		}
		if len(c.Blocks) > 0 {
			slim = append(slim, c)
		}
	}
	if len(slim) == 0 || slim[0].Role != llm.RoleUser {
		slim = append([]llm.Message{llm.TextMessage(llm.RoleUser, "(earlier conversation omitted)")}, slim...)
	}
	var ask strings.Builder
	ask.WriteString("Existing memory entries (name (type, scope): description):\n")
	if len(existing) == 0 {
		ask.WriteString("(none)\n")
	}
	for _, e := range existing {
		fmt.Fprintf(&ask, "- %s (%s, %s): %s\n", e.Name, e.Type, e.Scope, e.Description)
	}
	ask.WriteString("\nWrite the JSON lines for new or corrected knowledge now, or NONE.")
	slim = append(slim, llm.TextMessage(llm.RoleUser, ask.String()))
	resp, err := p.Stream(ctx, llm.Request{Model: model, System: extractSystem, Messages: slim, MaxTokens: 800}, nil)
	if err != nil {
		return nil, err
	}
	return ParseExtracted(resp.Message.Text()), nil
}

// ParseExtracted reads the extractor's output leniently: JSON objects one
// per line (fences and bullets tolerated), pretty-printed objects rejoined
// first, or plain lines as project facts.
func ParseExtracted(text string) []Extracted {
	text = joinPrettyJSON(text)
	var out []Extracted
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-* "))
		if line == "" || strings.HasPrefix(line, "```") || strings.EqualFold(strings.Trim(line, "."), "none") {
			continue
		}
		var x Extracted
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &x) == nil {
			if x.Description == "" && x.Body == "" {
				continue
			}
			out = append(out, x)
			continue
		}
		if len(line) > 20 && !strings.HasPrefix(line, "{") {
			out = append(out, Extracted{Description: Clean(line), Body: line})
		}
	}
	return out
}

// joinPrettyJSON rejoins a JSON object a model printed across several
// lines onto one, so the line-oriented parser reads it. Lines that are
// not part of an object pass through unchanged.
func joinPrettyJSON(text string) string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		switch {
		case depth == 0 && strings.HasPrefix(trim, "{") && !json.Valid([]byte(trim)):
			depth = 1
			cur.WriteString(trim)
		case depth > 0:
			cur.WriteString(trim)
			depth += strings.Count(trim, "{") - strings.Count(trim, "}")
			if depth <= 0 {
				depth = 0
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			out = append(out, line)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return strings.Join(out, "\n")
}

// Apply stores extracted candidates, all in project scope whatever scope
// the extractor asked for, and returns the entries that were created or
// changed.
func (s *Store) Apply(xs []Extracted) ([]Entry, error) { return s.ApplyWith(xs, "auto") }

// ApplyWith is Apply with an explicit source ("auto" for the turn
// extractor, "sleep" for the consolidation pass).
func (s *Store) ApplyWith(xs []Extracted, source string) ([]Entry, error) {
	var changed []Entry
	var errs []string
	reported := map[string]bool{}
	for _, x := range xs {
		// Always project scope: an extractor's guess at what applies to
		// every project turned one-off phrasings into global rules.
		e := Entry{
			Name: x.Name, Description: x.Description, Type: x.Type, Scope: "project",
			Body: x.Body, Source: source, Evidence: x.Evidence,
			Concerns: cleanConcerns(x.Concerns),
		}
		e, status, err := s.Put(e)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if status != Unchanged && !reported[e.Name] {
			reported[e.Name] = true
			changed = append(changed, e)
		}
		// A correction closes the entry it supersedes: the old file
		// stays on disk as history, marked with when it stopped being
		// true and what replaced it, instead of vanishing or silently
		// taking the correction's name. Replacing by the same name is a
		// plain update, which Put has already done.
		if x.Replaces != "" && Slug(x.Replaces) != e.Name {
			if old, ok := s.Get(x.Replaces); ok && old.Scope == "project" {
				if _, err := s.Close(old.Name, e.Name, time.Now()); err != nil {
					errs = append(errs, err.Error())
				}
			}
		}
	}
	if len(errs) > 0 {
		return changed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return changed, nil
}

// StampEvidence records where a batch of candidates was read from. ref
// is "<session-id>#~<seq>" for a prose window (the watermark form) or
// "<session-id>#<seq>" for one exact event.
func StampEvidence(xs []Extracted, ref string) []Extracted {
	for i := range xs {
		xs[i].Evidence = mergeEvidence(xs[i].Evidence, []string{ref})
	}
	return xs
}

// cleanConcerns clips a concerns list to something an entry file should
// carry: a handful of short path- or symbol-shaped strings.
func cleanConcerns(in []string) []string {
	var out []string
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		out = append(out, textfmt.Clip(c, 200))
		if len(out) == 8 {
			break
		}
	}
	return out
}
