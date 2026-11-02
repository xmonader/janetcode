package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/taint"
)

// Tool lets the model use its memory: recall entries relevant to a query,
// remember new knowledge, or forget an entry.
type Tool struct {
	Store *Store
	// NoArchive withdraws archive and restore (JANETCODE_OFF=memarchive),
	// leaving forget the only way to drop an entry.
	NoArchive bool
}

func (*Tool) Name() string { return "memory" }
func (t *Tool) Description() string {
	d := "Long-term memory across sessions. action \"recall\" returns the entries relevant to query (or every entry when query is empty). action \"remember\" saves knowledge worth keeping for future sessions: a user preference or correction (type feedback), a fact about the user (user), a decision and its reason (decision), a project fact that is not obvious from the code (project), or a pointer (reference); entries are kept for this project; a rule for every project is the user's to make with /remember -g, so suggest that when they state one. Give the same name to update an entry. action \"forget\" removes one by name. Do not record task progress or anything already in the index."
	if !t.NoArchive {
		d += " \"archive\" keeps an entry but hides it from recall (\"restore\" undoes it); prefer it to forget for what may only be stale."
	}
	return d
}
func (*Tool) Mutating() bool { return false }
func (t *Tool) Schema() json.RawMessage {
	actions, extra := `"recall","remember","forget"`, ""
	if !t.NoArchive {
		actions += `,"archive","restore"`
		extra = `
"include_archived":{"type":"boolean"},`
	}
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","enum":[` + actions + `]},` + extra + `
"query":{"type":"string","description":"recall: words to match against names, descriptions and bodies"},
"name":{"type":"string","description":"remember/forget: short kebab-case slug; reuse an existing name to update it"},
"description":{"type":"string","description":"remember: one line shown in the index"},
"body":{"type":"string","description":"remember: the knowledge, with why and how to apply it"},
"type":{"type":"string","enum":["user","feedback","project","decision","reference"]},
"scope":{"type":"string","enum":["project","global"],"description":"always stored as project; global needs the user's /remember -g"}},
"required":["action"],"additionalProperties":false}`)
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action, Query, Name, Description, Body, Type, Scope string
		IncludeArchived                                     bool `json:"include_archived"`
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
			for _, e := range t.Store.Entries() {
				if !e.Archived || (in.IncludeArchived && !t.NoArchive) {
					hits = append(hits, e)
				}
			}
		} else {
			hits = t.Store.SearchCtx(ctx, in.Query, 8)
			if in.IncludeArchived && !t.NoArchive {
				hits = append(hits, archivedMatches(t.Store, in.Query, hits)...)
			}
		}
		if len(hits) == 0 {
			return "Nothing remembered matches that.", nil
		}
		t.Store.Touch(hits...)
		var sb strings.Builder
		for _, e := range hits {
			r := Render(e)
			if e.Archived {
				r = strings.Replace(r, "<memory ", "<memory archived=\"true\" ", 1)
			}
			sb.WriteString(r + "\n")
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	case "remember":
		// The model never writes global memory: a rule every project
		// inherits is the user's call (/remember -g), not an inference.
		e, status, err := t.Store.Put(Entry{Name: in.Name, Description: in.Description, Body: in.Body, Type: in.Type, Scope: "project", Source: "model", Tainted: taint.From(ctx).Tainted()})
		if err != nil {
			return "", err
		}
		if status == Held {
			return fmt.Sprintf("Held for the user's confirmation: %s (%s): %s. It is not saved until they approve it with /memory approve; do not save it again.", e.Name, e.Type, e.Description), nil
		}
		verb := map[Status]string{Created: "Remembered", Updated: "Updated", Unchanged: "Already remembered"}[status]
		out := fmt.Sprintf("%s %s (%s, %s): %s", verb, e.Name, e.Type, e.Scope, e.Description)
		if in.Scope == "global" {
			out += "\nKept for this project only: memory for every project is written by the user with /remember -g. If this is meant for all projects, tell the user that."
		}
		return out, nil
	case "archive", "restore":
		if t.NoArchive {
			break
		}
		if in.Action == "archive" {
			e, err := t.Store.Archive(in.Name, true)
			if err != nil {
				return "", err
			}
			return "Archived " + e.Name + ": " + e.Description + ". It is kept but out of the index and recall; memory restore brings it back, forget deletes it.", nil
		}
		e, err := t.Store.Restore(in.Name)
		if err != nil {
			return "", err
		}
		return "Restored " + e.Name + ": " + e.Description, nil
	case "forget":
		e, err := t.Store.RemoveModel(in.Name)
		if err != nil {
			return "", err
		}
		return "Forgot " + e.Name + ": " + e.Description, nil
	}
	return "", fmt.Errorf("unknown action %q (want recall, remember or forget%s)", in.Action, map[bool]string{false: ", archive or restore", true: ""}[t.NoArchive])
}

// archivedMatches finds archived entries sharing a word with query that
// the ranked search (which skips them) did not return.
func archivedMatches(s *Store, query string, have []Entry) []Entry {
	seen := map[string]bool{}
	for _, e := range have {
		seen[e.Name] = true
	}
	want := map[string]bool{}
	for _, w := range Words(query) {
		want[w] = true
	}
	var out []Entry
	for _, e := range s.Entries() {
		if !e.Archived || seen[e.Name] {
			continue
		}
		for _, w := range Words(e.Name + " " + e.Description + " " + e.Body) {
			if want[w] {
				out = append(out, e)
				break
			}
		}
	}
	return out
}
