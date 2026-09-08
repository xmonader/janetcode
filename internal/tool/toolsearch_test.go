package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type namedTool struct{ name, desc string }

func (n namedTool) Name() string        { return n.name }
func (n namedTool) Description() string { return n.desc }
func (namedTool) Mutating() bool        { return false }
func (namedTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)
}
func (namedTool) Run(context.Context, json.RawMessage) (string, error) { return "ran", nil }

func TestDeferredToolsHiddenUntilSearched(t *testing.T) {
	r := NewRegistry(namedTool{"read_file", "Read a file"})
	r.AddDeferred(namedTool{"mcp__jira__create_issue", "Create a Jira issue in a project"})
	r.AddDeferred(namedTool{"mcp__jira__search", "Search Jira issues with JQL"})
	r.AddDeferred(namedTool{"mcp__slack__post", "Post a message to a Slack channel"})
	ts := &ToolSearch{Registry: r}
	r.Add(ts)
	if defs := r.Defs(); len(defs) != 2 || defs[0].Name != "read_file" || defs[1].Name != "tool_search" {
		t.Fatalf("defs = %+v", defs)
	}
	if !strings.Contains(ts.Description(), "3 deferred") || !strings.Contains(ts.Description(), "mcp__jira__search") {
		t.Fatalf("description = %q", ts.Description())
	}
	if _, ok := r.Get("mcp__jira__search"); !ok {
		t.Fatal("deferred tool must still be callable by name")
	}
	out, err := ts.Run(context.Background(), json.RawMessage(`{"query":"jira issue"}`))
	if err != nil || !strings.Contains(out, "## mcp__jira__create_issue") || !strings.Contains(out, "## mcp__jira__search") || strings.Contains(out, "slack") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !strings.Contains(out, `"q":{"type":"string"}`) {
		t.Fatal("schema missing from search result")
	}
	if defs := r.Defs(); len(defs) != 4 || len(r.Deferred()) != 1 {
		t.Fatalf("after activation defs=%d deferred=%d", len(defs), len(r.Deferred()))
	}
	out, _ = ts.Run(context.Background(), nil)
	if !strings.Contains(out, "1 deferred tools") || !strings.Contains(out, "mcp__slack__post\tPost a message") {
		t.Fatalf("list = %q", out)
	}
	out, _ = ts.Run(context.Background(), json.RawMessage(`{"query":"zzz"}`))
	if !strings.Contains(out, "No deferred tool matches") {
		t.Fatalf("miss = %q", out)
	}
	r.Remove("mcp__slack__post")
	if len(r.Deferred()) != 0 || !strings.Contains(ts.Description(), "Nothing is deferred") {
		t.Fatal("remove did not clear deferred entry")
	}
}
