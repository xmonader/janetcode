package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

// Tool adapts one server tool to the agent's tool interface.
type Tool struct {
	Client *Client
	Info   ToolInfo
	name   string
}

// AgentTools wraps every tool of the client, named mcp__<server>__<tool>.
// Servers with resources also get list_resources and read_resource tools.
func (c *Client) AgentTools() []tool.Tool {
	var out []tool.Tool
	for _, info := range c.tools {
		out = append(out, &Tool{Client: c, Info: info, name: ToolName(c.Name, info.Name)})
	}
	if c.Capabilities.Resources {
		out = append(out, &resourceListTool{c}, &resourceReadTool{c})
	}
	return out
}

// resourceListTool exposes resources/list to the model.
type resourceListTool struct{ c *Client }

func (t *resourceListTool) Name() string { return ToolName(t.c.Name, "list_resources") }
func (t *resourceListTool) Description() string {
	return fmt.Sprintf("[%s MCP server] List the resources (documents, data, URI templates) this server can provide. Read one with %s.", t.c.Name, ToolName(t.c.Name, "read_resource"))
}
func (t *resourceListTool) Mutating() bool { return false }
func (t *resourceListTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *resourceListTool) Run(ctx context.Context, _ json.RawMessage) (string, error) {
	items, err := t.c.ListResources(ctx)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "(no resources)", nil
	}
	var sb strings.Builder
	for _, r := range items {
		uri := r.URI
		if uri == "" {
			uri = r.URITemplate + " (template)"
		}
		fmt.Fprintf(&sb, "%s", uri)
		if r.Name != "" {
			fmt.Fprintf(&sb, " · %s", r.Name)
		}
		if r.MimeType != "" {
			fmt.Fprintf(&sb, " · %s", r.MimeType)
		}
		if r.Description != "" {
			fmt.Fprintf(&sb, "\n    %s", strings.TrimSpace(r.Description))
		}
		sb.WriteString("\n")
	}
	return tool.Truncate(strings.TrimRight(sb.String(), "\n")), nil
}

// resourceReadTool exposes resources/read to the model.
type resourceReadTool struct{ c *Client }

func (t *resourceReadTool) Name() string { return ToolName(t.c.Name, "read_resource") }
func (t *resourceReadTool) Description() string {
	return fmt.Sprintf("[%s MCP server] Read a resource by URI (see %s).", t.c.Name, ToolName(t.c.Name, "list_resources"))
}
func (t *resourceReadTool) Mutating() bool { return false }
func (t *resourceReadTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"uri":{"type":"string","description":"Resource URI"}},"required":["uri"],"additionalProperties":false}`)
}
func (t *resourceReadTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(input, &in); err != nil || strings.TrimSpace(in.URI) == "" {
		return "", fmt.Errorf("uri is required")
	}
	out, err := t.c.ReadResource(ctx, in.URI)
	if err != nil {
		return "", err
	}
	return tool.Truncate(out), nil
}

var badChars = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// ToolName builds the registry name: providers only accept [A-Za-z0-9_-]
// and at most 64 characters.
func ToolName(server, tool string) string {
	n := "mcp__" + badChars.ReplaceAllString(server, "_") + "__" + badChars.ReplaceAllString(tool, "_")
	if len(n) > 64 {
		n = n[:64]
	}
	return strings.TrimRight(n, "_-")
}

func (t *Tool) Name() string { return t.name }
func (t *Tool) Description() string {
	d := strings.TrimSpace(t.Info.Description)
	if d == "" {
		d = t.Info.Name
	}
	return fmt.Sprintf("[%s MCP server] %s", t.Client.Name, d)
}

// Mutating is true unless the server marks the tool read-only, so unknown
// side effects ask for permission.
func (t *Tool) Mutating() bool {
	ro := t.Info.Annotations.ReadOnlyHint
	return ro == nil || !*ro
}

func (t *Tool) Schema() json.RawMessage {
	s := t.Info.InputSchema
	if len(s) == 0 || string(s) == "null" {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return s
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	res, err := t.Client.CallTool(ctx, t.Info.Name, input)
	if err != nil {
		return "", err
	}
	if res.IsError {
		return "", fmt.Errorf("%s", firstNonEmpty(res.Text, "tool reported an error"))
	}
	return tool.Truncate(res.Text), nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
