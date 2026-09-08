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

// Tools wraps every tool of the client, named mcp__<server>__<tool>.
func (c *Client) AgentTools() []tool.Tool {
	var out []tool.Tool
	for _, info := range c.tools {
		out = append(out, &Tool{Client: c, Info: info, name: ToolName(c.Name, info.Name)})
	}
	return out
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
