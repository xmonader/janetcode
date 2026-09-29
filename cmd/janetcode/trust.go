package main

import (
	"strings"

	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/trust"
)

// trustNotice is the stderr line for a headless run in a project that
// ships capability files nobody has chosen a level for: they sit unused,
// and the line says how to let them in.
func trustNotice(g *trust.Gate, cwd string) string {
	if !g.NeedsDecision(cwd) {
		return ""
	}
	return "[trust] this project's " + strings.Join(capKinds(g.State(cwd)), ", ") + " are not used until you trust it: --trust instructions or --trust full for this run, or /trust in a session"
}

func capKinds(st trust.State) []string {
	seen := map[string]bool{}
	var kinds []string
	for _, c := range st.Caps {
		if !seen[c.Kind] {
			seen[c.Kind] = true
			kinds = append(kinds, c.Kind)
		}
	}
	return kinds
}

// registerEgressSubject adds `--inspect egress` and /inspect egress.
func registerEgressSubject(reg *inspect.Registry, ag interface{ EgressSummary() string }) {
	reg.AddText("egress", "what the last turn sent off this machine, and the egress policy", ag.EgressSummary)
}

// registerTrustSubject adds `--inspect trust` and /inspect trust: the
// effective level, where it came from and every capability it governs.
func registerTrustSubject(reg *inspect.Registry, g *trust.Gate, cwd func() string) {
	reg.AddText("trust", "project trust level (untrusted, instructions, full), where it came from, and the skills, hooks, MCP servers and other project files it governs", func() string {
		return g.State(cwd()).Report()
	})
}
