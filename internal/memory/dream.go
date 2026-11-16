package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

const dreamSystem = `You consolidate a coding agent's long-term memory. Merge overlapping entries of the same type into one clearer entry. Drop entries that are now obvious from the code or duplicated. Do not invent facts.

Output one JSON object per line, nothing else:
{"action":"keep","name":"existing-slug"}
{"action":"merge","name":"new-or-existing-slug","type":"project","description":"one line","body":"merged fact","replaces":["old-a","old-b"]}
{"action":"drop","name":"obsolete-slug"}
Output exactly NONE when the set is already tight.`

// dreamInboxNote is added to the prompt when /flush notes are waiting.
const dreamInboxNote = `

The input also holds an INBOX of long notes captured by /flush. Distil them: for each durable, reusable fact in a note that is not already covered by an entry, output a "merge" line with a new slug (type project, decision or reference; "replaces" empty unless an existing entry is made wrong or incomplete by it). Skip narration, task status and anything obvious from the code. One tight entry per fact, not one per note. An inbox note that holds nothing durable needs no line.`

const dreamMinEntries = 3

// DreamOp is one consolidation action. Distinct from Extracted: extract
// captures new facts, dream rewrites the store.
type DreamOp struct {
	Action      string
	Name        string
	Type        string
	Description string
	Body        string
	Replaces    []string
}

// Dream asks the model to merge overlapping memory entries. It does not
// replace Extract: extract captures new facts, dream tightens the store.
func Dream(ctx context.Context, p llm.Provider, model string, entries []Entry) ([]DreamOp, error) {
	return DreamWith(ctx, p, model, entries, nil)
}

// DreamWith is Dream that also reads the observations inbox: /flush notes
// are folded into merge ops next to the entry consolidation. The caller
// marks the notes consumed once the ops are applied (Store.MarkConsumed).
func DreamWith(ctx context.Context, p llm.Provider, model string, entries []Entry, inbox []Observation) ([]DreamOp, error) {
	if p == nil {
		return nil, fmt.Errorf("no provider")
	}
	if len(entries) < dreamMinEntries && len(inbox) == 0 {
		return nil, nil
	}
	var listing strings.Builder
	for _, e := range entries {
		if e.Source == "user" {
			continue // never drop or merge what the user wrote
		}
		fmt.Fprintf(&listing, "- %s (%s): %s\n  %s\n", e.Name, e.Type, e.Description, clip(e.Body, 400))
	}
	system := dreamSystem
	if len(inbox) > 0 {
		system += dreamInboxNote
		listing.WriteString("\nINBOX:\n")
		for _, o := range inbox {
			fmt.Fprintf(&listing, "--- note %s ---\n%s\n", o.ID, clip(o.Body, 3000))
		}
	}
	if listing.Len() == 0 {
		return nil, nil
	}
	dreamMsgs := []llm.Message{llm.TextMessage(llm.RoleUser, listing.String())}
	noteCall(ctx, fmt.Sprintf("dream: %s, %s", plural(len(entries), "memory entry"), plural(len(inbox), "inbox note")), system, dreamMsgs)
	resp, err := p.Stream(ctx, llm.Request{
		Model:     model,
		System:    system,
		Messages:  dreamMsgs,
		MaxTokens: 1200 + 400*min(len(inbox), 4),
		Effort:    "low",
	}, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	raw := strings.TrimSpace(resp.Message.Text())
	if raw == "" || strings.EqualFold(raw, "NONE") {
		return nil, nil
	}
	return parseDream(raw), nil
}

// ApplyDream applies merge/drop ops. User-authored entries are never
// removed.
func (s *Store) ApplyDream(ops []DreamOp) (changed int, err error) {
	for _, op := range ops {
		switch strings.ToLower(op.Action) {
		case "drop":
			if e, ok := s.Get(op.Name); ok && e.Source != "user" {
				if _, rerr := s.RemoveModel(op.Name); rerr == nil {
					changed++
				}
			}
		case "merge":
			_, _, err := s.Put(Entry{Name: op.Name, Description: op.Description, Type: op.Type, Scope: "project", Body: op.Body, Source: "auto"})
			if err != nil {
				return changed, err
			}
			changed++
			for _, name := range op.Replaces {
				name = strings.TrimSpace(name)
				if name == "" || strings.EqualFold(name, op.Name) {
					continue
				}
				if e, ok := s.Get(name); ok && e.Source != "user" {
					if _, rerr := s.RemoveModel(name); rerr != nil && !errors.Is(rerr, ErrUserEntry) {
						return changed, rerr
					}
				}
			}
		}
	}
	return changed, nil
}

func parseDream(raw string) []DreamOp {
	var out []DreamOp
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var row struct {
			Action      string   `json:"action"`
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			Description string   `json:"description"`
			Body        string   `json:"body"`
			Replaces    []string `json:"replaces"`
		}
		if json.Unmarshal([]byte(line), &row) != nil {
			continue
		}
		switch strings.ToLower(row.Action) {
		case "drop", "merge":
			out = append(out, DreamOp{
				Action: row.Action, Name: row.Name, Type: row.Type,
				Description: row.Description, Body: row.Body, Replaces: row.Replaces,
			})
		}
	}
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
