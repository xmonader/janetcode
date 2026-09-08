package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Tool lets the model use its memory: recall entries relevant to a query,
// remember new knowledge, or forget an entry.
type Tool struct {
	Store *Store
}

func (*Tool) Name() string { return "memory" }
func (*Tool) Description() string {
	return "Long-term memory across sessions. action \"recall\" returns the entries relevant to query (or every entry when query is empty). action \"remember\" saves knowledge worth keeping for future sessions: a user preference or correction (type feedback), a fact about the user (user), a decision and its reason (decision), a project fact that is not obvious from the code (project), or a pointer (reference); scope \"global\" for things about the user or their way of working, \"project\" (default) for this codebase. Give the same name to update an entry. action \"forget\" removes one by name. Do not record task progress or anything already in the index."
}
func (*Tool) Mutating() bool { return false }
func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":["recall","remember","forget"]},
"query":{"type":"string","description":"recall: words to match against names, descriptions and bodies"},
"name":{"type":"string","description":"remember/forget: short kebab-case slug; reuse an existing name to update it"},
"description":{"type":"string","description":"remember: one line shown in the index"},
"body":{"type":"string","description":"remember: the knowledge, with why and how to apply it"},
"type":{"type":"string","enum":["user","feedback","project","decision","reference"]},
"scope":{"type":"string","enum":["project","global"]}},
"required":["action"],"additionalProperties":false}`)
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action, Query, Name, Description, Body, Type, Scope string
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if t.Store == nil {
		return "", fmt.Errorf("memory is not available in this session")
	}
	switch in.Action {
	case "recall":
		var hits []Entry
		if strings.TrimSpace(in.Query) == "" {
			hits = t.Store.Entries()
		} else {
			hits = t.Store.Search(in.Query, 8)
		}
		if len(hits) == 0 {
			return "Nothing remembered matches that.", nil
		}
		t.Store.Touch(hits...)
		var sb strings.Builder
		for _, e := range hits {
			sb.WriteString(Render(e) + "\n")
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	case "remember":
		e, status, err := t.Store.Put(Entry{Name: in.Name, Description: in.Description, Body: in.Body, Type: in.Type, Scope: in.Scope, Source: "model"})
		if err != nil {
			return "", err
		}
		verb := map[Status]string{Created: "Remembered", Updated: "Updated", Unchanged: "Already remembered"}[status]
		return fmt.Sprintf("%s %s (%s, %s): %s", verb, e.Name, e.Type, e.Scope, e.Description), nil
	case "forget":
		e, err := t.Store.Remove(in.Name)
		if err != nil {
			return "", err
		}
		return "Forgot " + e.Name + ": " + e.Description, nil
	}
	return "", fmt.Errorf("unknown action %q (want recall, remember or forget)", in.Action)
}
