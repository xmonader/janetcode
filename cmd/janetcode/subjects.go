package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/inspect"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// Every part of a running session registers what it can say about itself
// here, so "why did it do that" is answered by looking rather than by
// guessing. Subjects report the live state, not a copy of it.

// subjectDeps is what the subjects read.
type subjectDeps struct {
	agent   *agent.Agent
	session *session.Session
	cfg     *config.Config
	cfgPath string
	cwd     string
	version string
	archive *agent.Archive
	mem     *memory.Store
	repo    *repomap.Cache
	diag    *lsp.Manager
	mcp     *mcptool.Manager
	jobs    *tool.Jobs
	task    *agent.Task
	skills  func() []skill.Skill
	// rules are the project's path-scoped rules; ruleInjector says which
	// of them this session has already given the model.
	rules        func() []rules.Rule
	ruleInjector *rules.Injector
}

// registerSubjects wires every subsystem into the registry. A subsystem
// that is not configured still registers and says so, so the list of what
// can be inspected does not change from session to session.
func registerSubjects(r *inspect.Registry, d subjectDeps) {
	r.AddText("context", "what the next request will carry, by part", func() string { return contextReport(d.agent) })
	r.AddText("session", "the session file, its metadata and size", func() string { return sessionReport(d.session, d.agent) })
	r.Add(inspect.Subject{
		Name: "events", Summary: "the append-only log of the session", Usage: "[N|type|seq]",
		Report: func(_ context.Context, arg string) (string, error) { return eventsReport(d.session, arg), nil },
	})
	r.Add(inspect.Subject{
		Name: "archive", Summary: "the full tool outputs kept for retrieval", Usage: "[id]",
		Report: func(_ context.Context, arg string) (string, error) { return archiveReport(d.archive, arg), nil },
	})
	r.AddText("retrieval", "every archive lookup this session made, and whether it found anything", func() string {
		return retrievalReport(d.session)
	})
	r.AddText("compaction", "when the history was summarised and what it cost", func() string { return compactionReport(d.session) })
	r.AddText("verdicts", "what each turn actually demonstrated, against what was claimed", func() string {
		return verdictsReport(d.session)
	})
	r.AddText("todos", "the plan the model is working from", func() string { return todosReport(d.agent) })
	r.AddText("prefix", "where the cached request prefix stopped matching, and why", func() string {
		if d.agent == nil {
			return "no agent"
		}
		return d.agent.PrefixReport()
	})
	r.AddText("cache", "what the prompt cache saved, and what was paid for twice", func() string {
		if d.agent == nil {
			return "no agent"
		}
		return d.agent.CacheReport()
	})
	r.AddText("scripts", "the scripts run for the model, and the calls they made", func() string { return scriptsReport(d.agent) })
	r.AddText("goals", "session goals and their rounds", func() string { return goalsReport(d.agent) })
	r.AddText("memory", "the working memory carried across compactions", func() string { return workingReport(d.agent) })
	r.AddText("tools", "the tools the model can call, with their sizes", func() string { return toolsReport(d.agent) })
	r.AddText("stats", "tool calls, model calls, timings, tokens and cost so far", func() string { return statsReport(d.agent) })
	r.AddText("trajectory", "the features the critic scores this turn on", func() string { return trajectoryReport(d.agent) })
	r.AddText("config", "the resolved configuration and where it came from", func() string {
		return configReport(d.cfg, d.cfgPath, d.version, d.cwd)
	})
	r.AddText("permissions", "what may run without asking", func() string { return permissionsReport(d.agent, d.cfg) })
	r.AddText("lsp", "language servers, their state and known problems", func() string { return lspReport(d.diag) })
	r.AddText("mcp", "MCP servers and the tools they contributed", func() string { return mcpReport(d.mcp) })
	r.AddText("jobs", "background jobs, running and finished", func() string { return jobsReport(d.jobs) })
	r.AddText("subagents", "subagents that can be continued, and what they spent", func() string { return subagentsReport(d.task) })
	r.AddText("skills", "the skills discovered for this project", func() string { return skillsReport(d.skills) })
	r.AddText("rules", "path-scoped project rules and whether they have been sent", func() string {
		return rulesReport(d.rules, d.ruleInjector)
	})
	r.AddText("notes", "the remembered notes available to the model", func() string { return notesReport(d.mem) })
	r.Add(inspect.Subject{
		Name: "repomap", Summary: "the repository map and how files rank", Usage: "[query]",
		Report: func(ctx context.Context, arg string) (string, error) { return repomapReport(ctx, d.repo, arg) },
	})
	r.AddText("undo", "file changes that can still be undone", func() string { return undoReport(d.agent) })
	r.Add(inspect.Subject{
		Name: "messages", Summary: "the conversation as the model will see it", Usage: "[N|#index]",
		Report: func(_ context.Context, arg string) (string, error) { return messagesReport(d.agent, arg), nil },
	})
	r.AddText("prompt", "the system prompt, verbatim", func() string { return promptReport(d.agent) })
	r.AddText("hooks", "the hooks that can interrupt a turn", func() string { return hooksReport(d.agent) })
	r.AddText("verify", "the checks a turn must pass before it is done", func() string { return verifyReport(d.agent) })
	r.AddText("providers", "the model endpoint in use and how requests are shaped", func() string {
		return providersReport(d.cfg, d.agent)
	})
	r.AddText("all", "every subject at once", func() string { return allReport(r) })
}

// allReport renders every subject, for a transcript of the whole state.
func allReport(r *inspect.Registry) string {
	var b strings.Builder
	for _, s := range r.Subjects() {
		if s.Name == "all" {
			continue
		}
		out, err := s.Report(context.Background(), "")
		if err != nil {
			out = "error: " + err.Error()
		}
		fmt.Fprintf(&b, "\n=== %s — %s ===\n%s\n", s.Name, s.Summary, strings.TrimRight(out, "\n"))
	}
	return strings.TrimSpace(b.String())
}

// Shared formatting. Reports are read by people and by the model, so they
// are plain text with aligned columns rather than JSON.

func pct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return n * 100 / total
}

func durMs(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return strconv.FormatInt(ms, 10) + "ms"
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

// registerOfflineSubjects is what can be reported without a working
// provider: the configuration itself, and why it is not working. Anything
// describing a live session says it needs one rather than being absent,
// so the list does not change shape depending on how the run started.
func registerOfflineSubjects(r *inspect.Registry, cfg *config.Config, cfgPath, cwd string, why error) {
	r.AddText("config", "the resolved configuration and where it came from", func() string {
		out := configReport(cfg, cfgPath, version, cwd)
		if why != nil {
			out += "\nthis run has no provider: " + why.Error() + "\n"
			out += "run janetcode (or janetcode --web) to pick a provider and paste a key, or set one in the environment (config: " + cfgPath + ")\n"
		}
		return out
	})
	r.AddText("providers", "the backends configured and the models they serve", func() string {
		if cfg == nil {
			return "no configuration"
		}
		var b strings.Builder
		if len(cfg.Providers) == 0 {
			b.WriteString("no providers are configured; run janetcode --init or set a key in the environment\n")
		}
		for _, name := range cfg.ProviderNames() {
			p := cfg.Providers[name]
			key := "no key"
			if len(p.Keys()) > 0 {
				key = fmt.Sprintf("%d key(s)", len(p.Keys()))
			}
			mark := "  "
			if name == cfg.Provider {
				mark = "* "
			}
			fmt.Fprintf(&b, "%s%-18s %-10s %-12s %s\n", mark, name, p.Type, key, p.BaseURL)
		}
		if routes := cfg.RoutableModels(); len(routes) > 0 {
			b.WriteString("\nmodels reachable by name:\n")
			for _, rt := range routes {
				fmt.Fprintf(&b, "  %-24s %-18s %s\n", rt.Model, rt.Provider, rt.Why)
			}
		}
		return b.String()
	})
	needsSession := func(name, summary string) {
		r.AddText(name, summary, func() string {
			return name + " describes a running session; this run has no provider, so there is none.\nSet a key, or run janetcode --init, and try again."
		})
	}
	for _, s := range []struct{ name, summary string }{
		{"context", "what the next request will carry, by part"},
		{"session", "the session file, its metadata and size"},
		{"events", "the append-only log of the session"},
		{"archive", "the full tool outputs kept for retrieval"},
		{"retrieval", "every archive lookup and whether it found anything"},
		{"verdicts", "what each turn demonstrated, against what it claimed"},
		{"messages", "the conversation as the model will see it"},
		{"tools", "the tools the model can call"},
		{"todos", "the plan the model is working from"},
		{"scripts", "the scripts run for the model, and the calls they made"},
		{"cache", "what the prompt cache saved, and what was paid for twice"},
		{"prefix", "where the cached request prefix stopped matching, and why"},
	} {
		needsSession(s.name, s.summary)
	}
}

// inspectOnce runs one subject and prints it, for the --inspect flag.
func inspectOnce(r *inspect.Registry, request string, out *os.File, offline bool) error {
	report, err := r.Report(context.Background(), request)
	var ns *inspect.NoSubjectError
	if errors.As(err, &ns) {
		msg := fmt.Sprintf("no --inspect subject %q", ns.Name)
		if ns.Near != "" {
			msg += fmt.Sprintf("; did you mean %q?", ns.Near)
		}
		if offline {
			return headless.BadInput("%s without an API key: subjects about a session need one (janetcode --inspect list shows what is available now)", msg)
		}
		return headless.BadInput("%s (janetcode --inspect list shows them all)", msg)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(out, report)
	return nil
}

// unused keeps imports honest when a subsystem is compiled out.
var _ = json.Marshal
var _ = filepath.Join
var _ = llm.RoleUser
