package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

func configReport(cfg *config.Config, path, version, cwd string) string {
	if cfg == nil {
		return "no configuration"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "janetcode %s in %s\n", version, cwd)
	fmt.Fprintf(&b, "config file %s", path)
	if _, err := os.Stat(path); err != nil {
		b.WriteString(" (not present; defaults and the environment decide)")
	}
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "  provider        %s\n", cfg.Provider)
	fmt.Fprintf(&b, "  model           %s\n", cfg.Model)
	fmt.Fprintf(&b, "  max tokens      %d\n", cfg.MaxTokens)
	fmt.Fprintf(&b, "  max iterations  %d\n", cfg.MaxIterations)
	fmt.Fprintf(&b, "  compact at      %s\n", compactAtText(cfg.CompactAt))
	fmt.Fprintf(&b, "  auto approve    %v\n", cfg.AutoApprove)
	fmt.Fprintf(&b, "  auto memory     %v\n", cfg.AutoMemory())
	if m := cfg.MetaFor(cfg.Model); m.ContextWindow > 0 {
		fmt.Fprintf(&b, "  context window  %s (from the model table)\n", textfmt.Tokens(m.ContextWindow))
	}
	// Where a key came from matters more than what it is.
	b.WriteString("\ncredentials:\n")
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "DASHSCOPE_API_KEY", "OPENAI_BASE_URL"} {
		if v := os.Getenv(env); v != "" {
			fmt.Fprintf(&b, "  %-18s set in the environment (%d characters)\n", env, len(v))
		}
	}
	if len(cfg.Providers) > 0 {
		b.WriteString("\nproviders configured:\n")
		names := make([]string, 0, len(cfg.Providers))
		for n := range cfg.Providers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := cfg.Providers[n]
			key := "no key"
			if p.APIKey != "" {
				key = "key set"
			}
			fmt.Fprintf(&b, "  %-12s %-10s %s\n", n, key, p.BaseURL)
		}
	}
	return b.String()
}

func compactAtText(n int) string {
	switch {
	case n < 0:
		return "never (compaction is off)"
	case n == 0:
		return fmt.Sprintf("%s (the default)", textfmt.Tokens(agent.DefaultCompactAt))
	}
	return textfmt.Tokens(n)
}

func lspReport(m *lsp.Manager) string {
	if m == nil || !m.Enabled() {
		return "no language server is configured for this project"
	}
	var b strings.Builder
	b.WriteString("language servers:\n")
	for _, s := range m.Status() {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	known := m.Known()
	if len(known) == 0 {
		b.WriteString("\nno problems reported so far\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\n%d known problems:\n", len(known))
	for i, k := range known {
		if i >= 20 {
			fmt.Fprintf(&b, "  … and %d more\n", len(known)-i)
			break
		}
		fmt.Fprintf(&b, "  %s\n", k)
	}
	return b.String()
}

func mcpReport(m *mcptool.Manager) string {
	if m == nil {
		return "no MCP servers in this session"
	}
	st := m.Status()
	if len(st) == 0 {
		return "no MCP servers are configured"
	}
	var b strings.Builder
	for _, s := range st {
		state := "ready"
		switch {
		case s.Err != nil:
			state = "failed"
		case s.Starting:
			state = "starting"
		}
		fmt.Fprintf(&b, "  %-16s %-9s %d tools", s.Name, state, len(s.Tools))
		if len(s.Prompts) > 0 {
			fmt.Fprintf(&b, ", %d prompts", len(s.Prompts))
		}
		b.WriteString("\n")
		if s.Err != nil {
			fmt.Fprintf(&b, "      %s\n", textfmt.Clip(strings.TrimSpace(s.Err.Error()), 90))
		}
		if len(s.Tools) > 0 {
			fmt.Fprintf(&b, "      %s\n", textfmt.Clip(strings.TrimSpace(strings.Join(s.Tools, ", ")), 90))
		}
	}
	return b.String()
}

func jobsReport(j *tool.Jobs) string {
	if j == nil {
		return "background jobs are not available in this session"
	}
	list := j.List()
	if len(list) == 0 {
		return "no background jobs"
	}
	var b strings.Builder
	for _, job := range list {
		state := "running"
		if job.Done {
			state = fmt.Sprintf("exited %d", job.Exit)
		}
		fmt.Fprintf(&b, "  %-4d %-12s %s\n       %s\n", job.ID, state, textfmt.Clip(strings.TrimSpace(job.Command), 90), job.OutPath)
	}
	return b.String()
}

func subagentsReport(t *agent.Task) string {
	if t == nil {
		return "the subagent tool is not in this session"
	}
	ids := t.ChildIDs()
	if len(ids) == 0 {
		return "no subagents have run"
	}
	u := t.Usage()
	var b strings.Builder
	fmt.Fprintf(&b, "%d subagents can be continued · they have spent in %s · out %s\n",
		len(ids), textfmt.Tokens(u.InputTokens+u.CacheReadTokens), textfmt.Tokens(u.OutputTokens))
	for _, id := range ids {
		fmt.Fprintf(&b, "  %s\n", id)
	}
	return b.String()
}

func skillsReport(load func() []skill.Skill) string {
	if load == nil {
		return "skills are not loaded in this session"
	}
	sk := load()
	if len(sk) == 0 {
		return "no skills found for this project"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d skills:\n", len(sk))
	for _, s := range sk {
		fmt.Fprintf(&b, "  %-24s %s\n", s.Name, textfmt.Clip(strings.TrimSpace(s.Description), 80))
	}
	return b.String()
}

// rulesReport lists the conditional instructions and what brings each
// in, marking the ones this session has already sent.
func rulesReport(load func() []rules.Rule, inj *rules.Injector) string {
	if load == nil {
		return "rules are not loaded in this session"
	}
	rs := load()
	if len(rs) == 0 {
		return "no path-scoped rules: add .janetcode/rules/<name>.md with a paths: list in its frontmatter"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d rules (each sent once, when a call touches a matching path):\n", len(rs))
	for _, r := range rs {
		state := "waiting"
		if inj.Sent(r.Name) {
			state = "sent"
		}
		fmt.Fprintf(&b, "  %-20s %-7s %-28s %s\n", r.Name, state, strings.Join(r.Paths, ","), textfmt.Clip(strings.TrimSpace(r.Description), 50))
	}
	return b.String()
}

func notesReport(m *memory.Store) string {
	if m == nil {
		return "memory is not enabled in this session"
	}
	if idx := strings.TrimSpace(m.Index()); idx != "" {
		return idx
	}
	return "nothing has been remembered for this project"
}

func repomapReport(ctx context.Context, c *repomap.Cache, query string) (string, error) {
	if c == nil {
		return "the repository map is not built in this session", nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ix, err := c.Get(rctx)
	if err != nil {
		return "", err
	}
	if ix == nil || len(ix.Files) == 0 {
		return "the repository map is empty", nil
	}
	if query == "" {
		return fmt.Sprintf("%d source files indexed (inspect repomap <query> to see how they rank for a request)", len(ix.Files)), nil
	}
	ranked := ix.Rank(query, nil)
	if len(ranked) == 0 {
		return "nothing ranks for " + query, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d files, ranked for %q:\n", len(ix.Files), query)
	for i, r := range ranked {
		if i >= 20 {
			break
		}
		why := ""
		if len(r.Why) > 0 {
			why = " — " + strings.Join(r.Why, ", ")
		}
		fmt.Fprintf(&b, "  %6.2f  %s%s\n", r.Score, r.File.Path, textfmt.Clip(strings.TrimSpace(why), 70))
	}
	return b.String(), nil
}
