package acp

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/mcp"
)

// mcpServerSpec is one entry of session/new's (and session/load's)
// mcpServers, in the ACP schema's shapes: a stdio server has command,
// args and env; an http or sse one has type, url and headers. Env and
// headers are lists of {name, value}.
type mcpServerSpec struct {
	Type    string      `json:"type,omitempty"`
	Name    string      `json:"name"`
	Command string      `json:"command,omitempty"`
	Args    []string    `json:"args,omitempty"`
	Env     []nameValue `json:"env,omitempty"`
	URL     string      `json:"url,omitempty"`
	Headers []nameValue `json:"headers,omitempty"`
}

type nameValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type mcpCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// clientServers turns the editor's list into server configs by name. An
// entry that is neither a command nor a URL, or has no name, is an error:
// the editor asked for a server it will expect to be there.
func clientServers(specs []mcpServerSpec) (map[string]mcp.ServerConfig, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make(map[string]mcp.ServerConfig, len(specs))
	for _, sp := range specs {
		name := strings.TrimSpace(sp.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: an mcpServers entry has no name", errBadParams)
		}
		var sc mcp.ServerConfig
		switch t := strings.ToLower(sp.Type); {
		case t == "http" || t == "sse":
			if sp.URL == "" {
				return nil, fmt.Errorf("%w: mcpServers %q: %s needs a url", errBadParams, name, t)
			}
			sc = mcp.ServerConfig{Type: t, URL: sp.URL, Headers: pairs(sp.Headers)}
		case t == "" || t == "stdio":
			if sp.Command == "" {
				return nil, fmt.Errorf("%w: mcpServers %q: a stdio server needs a command", errBadParams, name)
			}
			sc = mcp.ServerConfig{Command: sp.Command, Args: sp.Args, Env: pairs(sp.Env)}
		default:
			return nil, fmt.Errorf("%w: mcpServers %q: unknown type %q", errBadParams, name, sp.Type)
		}
		out[name] = sc
	}
	return out, nil
}

func pairs(nv []nameValue) map[string]string {
	if len(nv) == 0 {
		return nil
	}
	m := make(map[string]string, len(nv))
	for _, p := range nv {
		m[p.Name] = p.Value
	}
	return m
}
