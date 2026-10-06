package main

import (
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/hook"
)

// applySafeMode strips from cfg what runs code or decides for you on
// its own: your hooks and MCP servers, and auto-approval. The project's
// share (its hooks, MCP servers, verify gates and permission rules) is
// shut out by an empty trust store, and its instructions, skills, rules
// and profiles by the agent builder; see --safe-mode. It is for a
// repository you do not trust yet, and for telling whether a problem is
// janetcode's or something you added to it.
func applySafeMode(cfg *config.Config) {
	cfg.Hooks = hook.Config{}
	cfg.MCPServers = nil
	cfg.AutoApprove = false
}
