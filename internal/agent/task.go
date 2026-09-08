package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// Task is a tool that runs a subagent: a fresh conversation with the same
// model and the parent's read-only tools, which explores or researches and
// returns a report. It never modifies files, so several can run at once.
type Task struct {
	Parent *Agent
	// MaxIterations bounds the child's tool rounds. Zero means 25.
	MaxIterations int
	// Profiles, when set, supplies custom subagent definitions selectable
	// with the agent parameter.
	Profiles func() []skill.Profile
	// Root is the project directory; editing subagents check it out into
	// a git worktree and return their changes as a patch.
	Root string

	mu    sync.Mutex
	usage llm.Usage
}

func (*Task) Name() string { return "task" }
func (t *Task) Description() string {
	d := "Delegate a self-contained job to a subagent that has its own context window: exploring an unfamiliar area of the codebase, tracing how something works, or gathering facts across many files. By default it can read, list, glob, grep and fetch URLs but cannot edit files or run commands; with edit: true it works in an isolated git worktree and returns its changes as a patch for you to apply. Give it a complete brief with what to find and what to report back; it returns only its final report. Call several in one turn to work in parallel. Pass tools to restrict it to a few named tools when the job is narrow (fewer schemas, cheaper and more focused)."
	if ps := t.profiles(); len(ps) > 0 {
		var names []string
		for _, p := range ps {
			names = append(names, p.Name+" ("+p.Description+")")
		}
		d += " Custom agents, selectable with agent: " + strings.Join(names, "; ") + "."
	}
	return d
}

func (t *Task) profiles() []skill.Profile {
	if t.Profiles == nil {
		return nil
	}
	return t.Profiles()
}
func (*Task) Mutating() bool { return false }
func (*Task) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"description":{"type":"string","description":"Short label for what the subagent is doing (shown to the user)"},
"prompt":{"type":"string","description":"The full brief: goal, where to look, and exactly what to report"},
"tools":{"type":"array","items":{"type":"string"},"description":"Optional: only these read-only tools are given to the subagent, e.g. [\"grep\",\"read_file\"]"},
"agent":{"type":"string","description":"Optional: name of a custom agent definition to run the job as"},
"edit":{"type":"boolean","description":"Optional: let the subagent edit files and run commands in an isolated git worktree of the project. Its changes come back as a patch (also saved to a file) for you to review and apply with git apply; the real tree is untouched."}},
"required":["description","prompt"],"additionalProperties":false}`)
}

// Usage is the total spent by subagents so far.
func (t *Task) Usage() llm.Usage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.usage
}

func (t *Task) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Description string   `json:"description"`
		Prompt      string   `json:"prompt"`
		Tools       []string `json:"tools"`
		Agent       string   `json:"agent"`
		Edit        bool     `json:"edit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	if t.Parent == nil {
		return "", fmt.Errorf("task tool has no parent agent")
	}
	var profile *skill.Profile
	if in.Agent != "" {
		for _, p := range t.profiles() {
			if strings.EqualFold(p.Name, in.Agent) {
				pp := p
				profile = &pp
				break
			}
		}
		if profile == nil {
			return "", fmt.Errorf("no custom agent named %q", in.Agent)
		}
		if len(in.Tools) == 0 {
			in.Tools = profile.Tools
		}
	}
	var wt *worktree
	var child *Agent
	var err error
	if in.Edit {
		if t.Root == "" {
			return "", fmt.Errorf("editing subagents are not available: no project root")
		}
		if wt, err = newWorktree(ctx, t.Root); err != nil {
			return "", err
		}
		defer wt.remove(context.WithoutCancel(ctx))
		child = t.editingChild(wt)
	} else if child, err = t.child(in.Tools); err != nil {
		return "", err
	}
	if profile != nil {
		child.System += "\n\n# Agent: " + profile.Name + "\n" + profile.Body
		if profile.Model != "" {
			child.Model = profile.Model
		}
	}
	start := time.Now()
	err = child.Run(ctx, in.Prompt, nil)
	t.mu.Lock()
	t.usage.Add(child.Usage)
	t.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("subagent failed after %s: %w", time.Since(start).Round(time.Millisecond), err)
	}
	report := lastAssistantText(child.Messages)
	if strings.TrimSpace(report) == "" {
		report = "(the subagent finished without a report)"
	}
	if wt != nil {
		patch, stat, derr := wt.diff(context.WithoutCancel(ctx))
		switch {
		case derr != nil:
			report += "\n\n[could not collect the subagent's changes: " + derr.Error() + "]"
		case strings.TrimSpace(patch) == "":
			report += "\n\n[the subagent changed nothing in its worktree]"
		default:
			path, perr := savePatch(patch, in.Description)
			report += "\n\n## Changes made in the worktree (not applied)\n" + stat
			if perr == nil {
				report += "\n\nPatch saved to " + path + " — review it, then apply with: git apply " + path
			} else {
				report += "\n\n(could not save the patch to a file: " + perr.Error() + ")"
			}
			report += "\n\n```diff\n" + patch + "\n```"
		}
	}
	return fmt.Sprintf("%s\n\n[subagent %q: %d tool calls, %s, %s in / %s out]", report, in.Description, countToolUses(child.Messages), time.Since(start).Round(time.Millisecond), tokens(child.Usage.InputTokens+child.Usage.CacheReadTokens), tokens(child.Usage.OutputTokens)), nil
}

// editingChild builds a subagent with the full tool set rooted at the
// worktree. It runs without an approver: nothing it does reaches the real
// tree, and the patch is reviewed before it is applied. Deny rules still
// hold.
func (t *Task) editingChild(wt *worktree) *Agent {
	p := t.Parent
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 40
	}
	return &Agent{
		Provider:      p.Provider,
		Tools:         tool.NewRegistry(wt.tools()...),
		System:        p.System + "\n\nYou are a subagent doing one delegated job for the main agent, in your own checkout of the project. Make the change requested, run the relevant build or tests, and finish with a short report of what you changed and how you verified it. Your edits are returned to the main agent as a patch; do not commit. Do not ask questions; make reasonable assumptions and state them.",
		Model:         p.Model,
		MaxTokens:     p.MaxTokens,
		MaxIterations: maxIter,
		AutoApprove:   true,
		Rules:         perm.Rules{Deny: p.Rules.Deny},
		CompactAt:     p.CompactAt,
		ContextWindow: p.ContextWindow,
	}
}

// child builds the subagent: same provider and model, read-only tools only
// (or the named subset of them), no approver (a mutating tool would be
// denied outright).
func (t *Task) child(only []string) (*Agent, error) {
	p := t.Parent
	var ro []tool.Tool
	if len(only) > 0 {
		for _, name := range only {
			tl, ok := p.Tools.Get(name)
			if !ok {
				return nil, fmt.Errorf("unknown tool %q in tools", name)
			}
			if tl.Mutating() || name == t.Name() {
				return nil, fmt.Errorf("tool %q cannot be given to a subagent (read-only tools only)", name)
			}
			ro = append(ro, tl)
		}
	} else {
		for _, d := range p.Tools.Defs() {
			if tl, ok := p.Tools.Get(d.Name); ok && !tl.Mutating() && d.Name != t.Name() {
				ro = append(ro, tl)
			}
		}
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}
	return &Agent{
		Provider:      p.Provider,
		Tools:         tool.NewRegistry(ro...),
		System:        p.System + "\n\nYou are a subagent working on one delegated job for the main agent. You cannot edit files or run commands. Investigate with the tools you have, then reply with a complete, self-contained report of what you found: relevant file paths with line numbers, the facts requested, and anything surprising. Do not ask questions; make reasonable assumptions and state them.",
		Model:         p.Model,
		MaxTokens:     p.MaxTokens,
		MaxIterations: maxIter,
		CompactAt:     p.CompactAt,
		Archive:       p.Archive,
		AutoApprove:   false,
		Debug:         p.Debug,
	}, nil
}

func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			if t := msgs[i].Text(); t != "" {
				return t
			}
		}
	}
	return ""
}

func countToolUses(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.ToolUses())
	}
	return n
}

func tokens(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
