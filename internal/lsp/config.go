package lsp

import (
	"fmt"
	"strings"
)

// Server describes how to start one language server.
type Server struct {
	// Command is the executable and its arguments.
	Command []string `json:"command"`
	// Extensions lists the file extensions (".go") the server handles.
	Extensions []string `json:"extensions"`
}

// Config is the "lsp" section of the config file.
type Config struct {
	// Disabled turns diagnostics off entirely.
	Disabled bool `json:"disabled,omitempty"`
	// Servers adds to or overrides the defaults by name. A server with an
	// empty command removes a default.
	Servers map[string]Server `json:"servers,omitempty"`
	// Timeout is how many seconds to wait for diagnostics after an edit;
	// zero means 8.
	Timeout int `json:"timeout,omitempty"`
}

// Validate rejects servers that have extensions but no command.
func (c Config) Validate() error {
	for name, s := range c.Servers {
		if len(s.Command) == 0 && len(s.Extensions) > 0 {
			return fmt.Errorf("lsp: server %q has extensions but no command", name)
		}
		for _, e := range s.Extensions {
			if !strings.HasPrefix(e, ".") {
				return fmt.Errorf("lsp: server %q extension %q must start with a dot", name, e)
			}
		}
	}
	return nil
}

// Defaults are the servers used when the binary is on PATH.
var Defaults = map[string]Server{
	"gopls":                      {Command: []string{"gopls"}, Extensions: []string{".go"}},
	"pyright":                    {Command: []string{"pyright-langserver", "--stdio"}, Extensions: []string{".py", ".pyi"}},
	"typescript-language-server": {Command: []string{"typescript-language-server", "--stdio"}, Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
	"rust-analyzer":              {Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}},
	"clangd":                     {Command: []string{"clangd"}, Extensions: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx"}},
}

// Active merges the defaults with the config's overrides.
func (c Config) Active() map[string]Server {
	out := map[string]Server{}
	for k, v := range Defaults {
		out[k] = v
	}
	for k, v := range c.Servers {
		if len(v.Command) == 0 {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}
