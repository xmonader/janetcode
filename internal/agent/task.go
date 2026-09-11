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
	// Models, when set, resolves a model name to the provider that serves
	// it, so a job can be given to a different model than the one running
	// the session: a cheap one to read a lot of code, a strong one for
	// the part that has to be right. Without it the model parameter is
	// refused rather than silently ignored.
	Models func(model string) (llm.Provider, string, error)

	mu       sync.Mutex
	usage    llm.Usage
	children map[string]*subagent
	order    []string // insertion order, for eviction
	nextID   int
}

// maxChildren bounds how many subagents stay continuable. Each holds its
// whole transcript (and, when it edits, a checked-out worktree), so a
// session that delegates all day does not keep every one of them alive.
const maxChildren = 8

// subagent is a continuable child: the agent, the usage already billed to
// the parent, and the worktree it edits in (nil for read-only subagents).
type subagent struct {
	agent *Agent
	wt    *worktree
	seen  llm.Usage
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
"edit":{"type":"boolean","description":"Optional: let the subagent edit files and run commands in an isolated git worktree of the project. Its changes come back as a patch (also saved to a file) for you to review and apply with git apply; the real tree is untouched."},
"model":{"type":"string","description":"Optional: run the job on this model instead of the session's, by name (e.g. glm-5). Use a cheaper or faster model for wide reading, or a stronger one for a job that has to be right."},
"continue":{"type":"string","description":"Optional: id of a previous subagent to send another prompt. Omit prompt to list ids."}},
"required":["description","prompt"],"additionalProperties":false}`)
}

// ChildIDs lists continuable subagents started from this parent.
func (t *Task) ChildIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.order...)
}

// Close removes the worktrees of every editing subagent. Call it when the
// parent session ends.
func (t *Task) Close(ctx context.Context) {
	t.mu.Lock()
	kids := t.children
	t.children, t.order = nil, nil
	t.mu.Unlock()
	for _, c := range kids {
		if c.wt != nil {
			c.wt.remove(ctx)
		}
	}
}

// remember registers a continuable subagent, evicting the oldest (and its
// worktree) once there are too many.
func (t *Task) remember(id string, c *subagent) {
	t.mu.Lock()
	if t.children == nil {
		t.children = map[string]*subagent{}
	}
	t.children[id] = c
	t.order = append(t.order, id)
	var evicted *subagent
	if len(t.order) > maxChildren {
		old := t.order[0]
		t.order = t.order[1:]
		evicted = t.children[old]
		delete(t.children, old)
	}
	t.mu.Unlock()
	if evicted != nil && evicted.wt != nil {
		evicted.wt.remove(context.Background())
	}
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
		Model       string   `json:"model"`
		Continue    string   `json:"continue"`
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
	var kid *subagent
	var err error
	var childID string
	if in.Continue != "" {
		t.mu.Lock()
		kid = t.children[in.Continue]
		t.mu.Unlock()
		if kid == nil {
			return "", fmt.Errorf("no subagent %q (it may have been retired; start a new one)", in.Continue)
		}
		childID = in.Continue
	} else if in.Edit {
		if t.Root == "" {
			return "", fmt.Errorf("editing subagents are not available: no project root")
		}
		// The worktree outlives this call: the subagent is continuable, and
		// a continued run reads and edits the same checkout. Close (or
		// eviction) removes it.
		wt, werr := newWorktree(ctx, t.Root)
		if werr != nil {
			return "", werr
		}
		kid = &subagent{agent: t.editingChild(wt, profile), wt: wt}
	} else {
		ag, cerr := t.child(in.Tools, profile)
		if cerr != nil {
			return "", cerr
		}
		kid = &subagent{agent: ag}
	}
	if profile != nil && in.Continue == "" {
		kid.agent.System += "\n\n# Agent: " + profile.Name + "\n" + profile.Body
		// A profile names a model for the setup it was written for, and
		// worked before there was any routing; it keeps the session's
		// provider unless the caller asked for a model by name.
		if profile.Model != "" && in.Model == "" {
			kid.agent.Model = profile.Model
			if t.Models != nil {
				if prov, resolved, err := t.Models(profile.Model); err == nil {
					kid.agent.Provider, kid.agent.Model = prov, resolved
				}
			}
		}
	}
	// A model asked for by name is resolved to whatever backend serves
	// it, which may not be the one running this session. Refusing is
	// better than sending the name to an endpoint that does not have it.
	if in.Model != "" && in.Continue == "" {
		if t.Models == nil {
			return "", fmt.Errorf("this session cannot route to another model; run the job without model, or switch the session with /model")
		}
		prov, resolved, err := t.Models(in.Model)
		if err != nil {
			return "", err
		}
		kid.agent.Provider, kid.agent.Model = prov, resolved
	}
	if childID == "" {
		t.mu.Lock()
		t.nextID++
		childID = fmt.Sprintf("task-%d", t.nextID)
		t.mu.Unlock()
		t.remember(childID, kid)
	}
	child, wt := kid.agent, kid.wt
	start := time.Now()
	// A profile's time budget bounds the job, not the session: the
	// parent's turn is held open for as long as the subagent runs, so a
	// subagent that will not finish has to be finished for it.
	runCtx := ctx
	if profile != nil && profile.MaxMinutes > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(profile.MaxMinutes)*time.Minute)
		defer cancel()
	}
	err = child.Run(runCtx, in.Prompt, nil)
	if err != nil && ctx.Err() == nil && runCtx.Err() != nil {
		// Out of time rather than cancelled: whatever it found so far is
		// worth more to the parent than an error with nothing in it.
		report := lastAssistantText(child.Messages)
		if strings.TrimSpace(report) == "" {
			report = "(the subagent ran out of time before reporting anything)"
		}
		t.mu.Lock()
		t.usage.Add(usageSince(kid.seen, child.Usage))
		kid.seen = child.Usage
		t.mu.Unlock()
		return fmt.Sprintf("%s\n\n[subagent %q id=%s stopped after its %d-minute budget; continue it with task continue=%s]", report, in.Description, childID, profile.MaxMinutes, childID), nil
	}
	// Usage is cumulative over the subagent's life, so only what this
	// exchange added is billed to the parent.
	t.mu.Lock()
	t.usage.Add(usageSince(kid.seen, child.Usage))
	kid.seen = child.Usage
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
	on := ""
	if child.Model != "" && child.Model != t.Parent.Model {
		on = " on " + child.Model
	}
	return fmt.Sprintf("%s\n\n[subagent %q id=%s%s: %d tool calls, %s, %s in / %s out; continue with task continue=%s]", report, in.Description, childID, on, countToolUses(child.Messages), time.Since(start).Round(time.Millisecond), tokens(child.Usage.InputTokens+child.Usage.CacheReadTokens), tokens(child.Usage.OutputTokens), childID), nil
}

// editingChild builds a subagent with the full tool set rooted at the
// worktree. It runs without an approver: nothing it does reaches the real
// tree, and the patch is reviewed before it is applied. Deny rules still
// hold.
func (t *Task) editingChild(wt *worktree, profile *skill.Profile) *Agent {
	p := t.Parent
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 40
	}
	if profile != nil && profile.MaxTurns > 0 {
		maxIter = profile.MaxTurns
	}
	child := &Agent{
		Provider:      p.Provider,
		Tools:         tool.NewRegistry(allowed(wt.tools(), profile)...),
		System:        p.System + "\n\nYou are a subagent doing one delegated job for the main agent, in your own checkout of the project. Make the change requested, run the relevant build or tests, and finish with a short report of what you changed and how you verified it. Your edits are returned to the main agent as a patch; do not commit. Do not ask questions; make reasonable assumptions and state them.",
		Model:         p.Model,
		MaxTokens:     p.MaxTokens,
		MaxIterations: maxIter,
		AutoApprove:   true,
		Rules:         perm.Rules{Deny: p.Rules.Deny},
		CompactAt:     p.CompactAt,
		ContextWindow: p.ContextWindow,
	}
	// An editing subagent approves its own calls because nothing it does
	// reaches the real tree and the patch is reviewed before it is
	// applied. A profile can ask for the other arrangement: the
	// confirmation is queued to the parent's approver — the user — which
	// is the difference between a job that stops on a question and one
	// that quietly answers it for itself.
	if profile != nil && profile.Bubble() && p.Approver != nil {
		child.AutoApprove = false
		child.Approver = bubbled{to: p.Approver, name: profile.Name}
	}
	return child
}

// bubbled sends a subagent's confirmation to the parent's approver,
// saying which subagent is asking: a prompt that does not say who wants
// the command is a prompt nobody can answer.
type bubbled struct {
	to   Approver
	name string
}

func (b bubbled) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	if call.Intent == "" {
		call.Intent = "asked for by the " + b.name + " subagent"
	} else {
		call.Intent = "[" + b.name + " subagent] " + call.Intent
	}
	return b.to.Approve(ctx, call)
}

// allowed drops the tools a profile refuses. It runs after the
// allowlist, so a tool a profile denies cannot be let back in by naming
// it in tools.
func allowed(ts []tool.Tool, profile *skill.Profile) []tool.Tool {
	if profile == nil || len(profile.Disallowed) == 0 {
		return ts
	}
	deny := map[string]bool{}
	for _, n := range profile.Disallowed {
		deny[strings.ToLower(strings.TrimSpace(n))] = true
	}
	out := make([]tool.Tool, 0, len(ts))
	for _, tl := range ts {
		if !deny[strings.ToLower(tl.Name())] {
			out = append(out, tl)
		}
	}
	return out
}

// child builds the subagent: same provider and model, read-only tools only
// (or the named subset of them), no approver (a mutating tool would be
// denied outright).
func (t *Task) child(only []string, profile *skill.Profile) (*Agent, error) {
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
			if _, isGoal := tl.(*GoalStatus); isGoal {
				return nil, fmt.Errorf("tool %q cannot be given to a subagent: it would end the main session's goal", name)
			}
			ro = append(ro, tl)
		}
	} else {
		for _, d := range p.Tools.Defs() {
			tl, ok := p.Tools.Get(d.Name)
			if !ok || tl.Mutating() || d.Name == t.Name() {
				continue
			}
			// goal_status is read-only but writes the parent's goal log.
			if _, isGoal := tl.(*GoalStatus); isGoal {
				continue
			}
			ro = append(ro, tl)
		}
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 25
	}
	if profile != nil && profile.MaxTurns > 0 {
		maxIter = profile.MaxTurns
	}
	if ro = allowed(ro, profile); len(ro) == 0 {
		return nil, fmt.Errorf("the %s agent has no tools left once its disallowed_tools are removed", profile.Name)
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

// usageSince is the usage added between two cumulative snapshots.
func usageSince(before, now llm.Usage) llm.Usage {
	return llm.Usage{
		InputTokens:      now.InputTokens - before.InputTokens,
		OutputTokens:     now.OutputTokens - before.OutputTokens,
		CacheReadTokens:  now.CacheReadTokens - before.CacheReadTokens,
		CacheWriteTokens: now.CacheWriteTokens - before.CacheWriteTokens,
		CostUSD:          now.CostUSD - before.CostUSD,
	}
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
