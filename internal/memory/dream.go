package memory

import (
	"context"
	"encoding/json"
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

// Dream asks the model to merge overlapping memory entries. It does not
// replace Extract: extract captures new facts, dream tightens the store.
func Dream(ctx context.Context, p llm.Provider, model string, entries []Entry) ([]Extracted, error) {
	if p == nil {
		return nil, fmt.Errorf("no provider")
	}
	if len(entries) < 3 {
		return nil, nil
	}
	var listing strings.Builder
	for _, e := range entries {
		if e.Source == "user" {
			continue // never drop or merge what the user wrote
		}
		fmt.Fprintf(&listing, "- %s (%s): %s\n  %s\n", e.Name, e.Type, e.Description, textfmtClip(e.Body, 400))
	}
	if listing.Len() == 0 {
		return nil, nil
	}
	resp, err := p.Stream(ctx, llm.Request{
		Model:     model,
		System:    dreamSystem,
		Messages:  []llm.Message{llm.TextMessage(llm.RoleUser, listing.String())},
		MaxTokens: 1200,
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
func (s *Store) ApplyDream(ops []Extracted) (changed int, err error) {
	for _, x := range ops {
		if x.Body == "" && x.Name != "" {
			if e, ok := s.Get(x.Name); ok && e.Source != "user" {
				if _, rerr := s.Remove(x.Name); rerr == nil {
					changed++
				}
			}
			continue
		}
		_, _, err := s.Put(Entry{Name: x.Name, Description: x.Description, Type: x.Type, Scope: "project", Body: x.Body, Source: "auto"})
		if err != nil {
			return changed, err
		}
		changed++
		for _, name := range strings.Split(x.Replaces, ",") {
			name = strings.TrimSpace(name)
			if name == "" || strings.EqualFold(name, x.Name) {
				continue
			}
			if e, ok := s.Get(name); ok && e.Source != "user" {
				_, _ = s.Remove(name)
			}
		}
	}
	return changed, nil
}

func parseDream(raw string) []Extracted {
	var out []Extracted
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
		case "drop":
			if row.Name != "" {
				out = append(out, Extracted{Name: row.Name, Replaces: row.Name})
			}
		case "merge":
			repl := strings.Join(row.Replaces, ",")
			out = append(out, Extracted{
				Type: row.Type, Name: row.Name, Description: row.Description,
				Body: row.Body, Replaces: repl, Scope: "project",
			})
		}
	}
	return out
}

func textfmtClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
