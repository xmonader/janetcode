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
	life     context.Context
	stop     context.CancelFunc
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

	desc    string
	started time.Time
	done    bool
	// last is the most recent thing the child was seen doing, kept so a
	// UI can answer "what is it up to" without waiting for the report.
	last string
	fork bool
}

// ChildInfo is a live snapshot of one subagent for status lines, /agents
// and the web workspace panel.
type ChildInfo struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Started     time.Time `json:"started"`
	Done        bool      `json:"done"`
	Last        string    `json:"last"`
	Editing     bool      `json:"editing"`
	Fork        bool      `json:"fork"`
	Usage       llm.Usage `json:"usage"`
}

// Children lists subagents started from this parent, oldest first.
func (t *Task) Children() []ChildInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ChildInfo, 0, len(t.order))
	for _, id := range t.order {
		c := t.children[id]
		if c == nil {
			continue
		}
		out = append(out, childInfoLocked(id, c))
	}
	return out
}

func childInfoLocked(id string, c *subagent) ChildInfo {
	return ChildInfo{
		ID: id, Description: c.desc, Started: c.started, Done: c.done,
		Last: c.last, Editing: c.wt != nil, Fork: c.fork, Usage: c.agent.Usage,
	}
}

// ChildStep is one line of a subagent's own conversation, for a UI that
// shows what it did rather than only that it is running.
type ChildStep struct {
	Kind    string `json:"kind"` // prompt, think, text, tool, now
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	Result  string `json:"result,omitempty"`
	OK      bool   `json:"ok"`
	Pending bool   `json:"pending,omitempty"`
}

// ChildLog is a live snapshot of one subagent, including a copy of its
// messages so a UI can render the transcript without holding the lock.
type ChildLog struct {
	ChildInfo
	Messages []llm.Message
}

// ChildLog returns one continuable subagent and a copy of its transcript.
func (t *Task) ChildLog(id string) (ChildLog, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := t.children[id]
	if c == nil {
		return ChildLog{}, false
	}
	msgs := c.agent.Messages
	cp := make([]llm.Message, len(msgs))
	copy(cp, msgs)
	return ChildLog{ChildInfo: childInfoLocked(id, c), Messages: cp}, true
}

// Steps walks the child's messages into display order: the brief, each
// thought and answer, each tool call with its result, and what it is
// doing now if it has not finished.
func (c ChildLog) Steps() []ChildStep {
	results := map[string]llm.Block{}
	for _, msg := range c.Messages {
		for _, b := range msg.Blocks {
			if b.Type == llm.BlockToolResult {
				results[b.ToolUseID] = b
			}
		}
	}
	var out []ChildStep
	for _, msg := range c.Messages {
		switch msg.Role {
		case llm.RoleUser:
			if t := strings.TrimSpace(msg.Text()); t != "" {
				out = append(out, ChildStep{Kind: "prompt", Text: t})
			}
		case llm.RoleAssistant:
			for _, b := range msg.Blocks {
				switch b.Type {
				case llm.BlockThinking:
					if t := strings.TrimSpace(b.Text); t != "" {
						out = append(out, ChildStep{Kind: "think", Text: t})
					}
				case llm.BlockText:
					if t := strings.TrimSpace(b.Text); t != "" {
						out = append(out, ChildStep{Kind: "text", Text: t})
					}
				case llm.BlockToolUse:
					st := ChildStep{Kind: "tool", Name: b.Name, Text: briefInput(string(b.Input)), Pending: true}
					if res, ok := results[b.ID]; ok {
						st.Pending = false
						st.OK = !res.IsError
						st.Result = res.Text
					}
					out = append(out, st)
				}
			}
		}
	}
	if !c.Done && c.Last != "" {
		out = append(out, ChildStep{Kind: "now", Text: c.Last})
	}
	return out
}

func (*Task) Name() string { return "task" }
func (t *Task) Description() string {
	d := "Delegate a self-contained job to a subagent that has its own context window: exploring an unfamiliar area of the codebase, tracing how something works, or gathering facts across many files. By default it can read, list, glob, grep and fetch URLs but cannot edit files or run commands; with edit: true it works in an isolated git worktree and returns its changes as a patch for you to apply. With fork: true it inherits this conversation's history, the same tools (except it cannot fork again), and runs in the background; continue it by id. Give it a complete brief with what to find and what to report back; it returns only its final report. Call several in one turn to work in parallel. Pass tools to restrict it to a few named tools when the job is narrow (fewer schemas, cheaper and more focused)."
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
	var user []skill.Profile
	if t.Profiles != nil {
		user = t.Profiles()
	}
	return skill.MergeProfiles(skill.Builtins(), user)
}
func (*Task) Mutating() bool { return false }
func (*Task) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"description":{"type":"string","description":"Short label for what the subagent is doing (shown to the user)"},
"prompt":{"type":"string","description":"The full brief: goal, where to look, and exactly what to report"},
"tools":{"type":"array","items":{"type":"string"},"description":"Optional: only these read-only tools are given to the subagent, e.g. [\"grep\",\"read_file\"]"},
"agent":{"type":"string","description":"Optional: name of a subagent profile. explore and general are built in (read-only); others come from .janetcode/agents/<name>.md"},
"edit":{"type":"boolean","description":"Optional: let the subagent edit files and run commands in an isolated git worktree of the project. Its changes come back as a patch (also saved to a file) for you to review and apply with git apply; the real tree is untouched."},
"model":{"type":"string","description":"Optional: run the job on this model instead of the session's, by name (e.g. glm-5). Use a cheaper or faster model for wide reading, or a stronger one for a job that has to be right."},
"continue":{"type":"string","description":"Optional: id of a previous subagent to send another prompt. Omit prompt to list ids."},
"fork":{"type":"boolean","description":"Optional: inherit this conversation's history and tools, run in the background, and return an id immediately. Cannot nest, and cannot be combined with edit."}},
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
	if t.stop != nil {
		t.stop()
		t.stop = nil
		t.life = nil
	}
	kids := t.children
	t.children, t.order = nil, nil
	t.mu.Unlock()
	for _, c := range kids {
		if c.wt != nil {
			c.wt.remove(ctx)
		}
	}
}

func (t *Task) sessionCtx() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.life == nil {
		t.life, t.stop = context.WithCancel(context.Background())
	}
	return t.life
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
	var in TaskInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	if t.Parent == nil {
		return "", fmt.Errorf("task tool has no parent agent")
	}
	if in.Fork && in.Continue != "" {
		in.Fork = false
	}
	if in.Fork && in.Edit {
		return "", fmt.Errorf("fork cannot be combined with edit: a fork inherits the session, an editing subagent uses a worktree")
	}
	if in.Fork && t.Parent.Forked {
		return "", fmt.Errorf("a fork cannot spawn another fork")
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
	// A model asked for by name is resolved to whatever backend serves it,
	// which may not be the one running this session. Refusing is better
	// than sending the name to an endpoint that does not have it — and it
	// is resolved here, before anything is built, because an editing job
	// checks the project out first and a later refusal would leave that
	// worktree and its base snapshot behind with nothing holding them.
	var routedProv llm.Provider
	var routedModel string
	if in.Model != "" && in.Continue == "" {
		if t.Models == nil {
			return "", fmt.Errorf("this session cannot route to another model; run the job without model, or switch the session with /model")
		}
		prov, resolved, err := t.Models(in.Model)
		if err != nil {
			return "", err
		}
		routedProv, routedModel = prov, resolved
	}
	var kid *subagent
	var err error
	var childID string
	if in.Continue != "" {
		t.mu.Lock()
		kid = t.children[in.Continue]
		running := kid != nil && !kid.done
		last := ""
		if kid != nil {
			last = kid.last
		}
		t.mu.Unlock()
		if kid == nil {
			return "", fmt.Errorf("no subagent %q (it may have been retired; start a new one)", in.Continue)
		}
		if running {
			return fmt.Sprintf("subagent %s is still running (%s); wait or watch with /agents", in.Continue, last), nil
		}
		childID = in.Continue
	} else if in.Fork {
		ag, ferr := t.forkChild()
		if ferr != nil {
			return "", ferr
		}
		kid = &subagent{agent: ag, fork: true}
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
	if routedProv != nil {
		kid.agent.Provider, kid.agent.Model = routedProv, routedModel
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
	t.mu.Lock()
	kid.desc = in.Description
	kid.started = start
	kid.done = false
	kid.last = "starting"
	t.mu.Unlock()
	emit := func(ev Event) {
		t.mu.Lock()
		defer t.mu.Unlock()
		switch ev.Kind {
		case EvToolRun:
			if ev.Call != nil {
				kid.last = ev.Call.Name + " " + briefInput(string(ev.Call.Input))
			}
		case EvPhase:
			kid.last = ev.Text
		}
	}
	if kid.fork && in.Continue == "" {
		// A fork must outlive this tool call: the parent turn continues
		// and the child is watched with /agents. The session context is
		// cancelled from Close, so quitting the TUI stops it.
		bg := t.sessionCtx()
		var cancel context.CancelFunc
		if profile != nil && profile.MaxMinutes > 0 {
			bg, cancel = context.WithTimeout(bg, time.Duration(profile.MaxMinutes)*time.Minute)
		}
		go func() {
			if cancel != nil {
				defer cancel()
			}
			_ = child.Run(bg, in.Prompt, emit)
			t.mu.Lock()
			kid.done = true
			t.usage.Add(usageSince(kid.seen, child.Usage))
			kid.seen = child.Usage
			t.mu.Unlock()
		}()
		return fmt.Sprintf("started fork %q id=%s in the background; watch with /agents, continue with task continue=%s", in.Description, childID, childID), nil
	}
	err = child.Run(runCtx, in.Prompt, emit)
	t.mu.Lock()
	kid.done = true
	t.mu.Unlock()
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
		Ledger:        p.Ledger,
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

// parentOnlyTool reports tools that stay on the main session even when they
// are not mutating: they write the parent's logs, ask the user, or spawn
// more children through the parent's task tool.
func parentOnlyTool(tl tool.Tool) bool {
	switch tl.(type) {
	case *GoalStatus, *AskTool, *Workflow:
		return true
	}
	return false
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
			if parentOnlyTool(tl) {
				return nil, fmt.Errorf("tool %q cannot be given to a subagent", name)
			}
			ro = append(ro, tl)
		}
	} else {
		for _, d := range p.Tools.Defs() {
			tl, ok := p.Tools.Get(d.Name)
			if !ok || tl.Mutating() || d.Name == t.Name() || parentOnlyTool(tl) {
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
		// A deny rule holds for a subagent too, as it does for the
		// editing one. Deny is consulted for read-only tools as well, so
		// without this a path the user refused could be read simply by
		// delegating the job. The allow list is deliberately not passed
		// on: permission granted in the session is not permission the
		// subagent asked for.
		Rules:  perm.Rules{Deny: p.Rules.Deny},
		Debug:  p.Debug,
		Ledger: p.Ledger,
	}, nil
}

// forkChild builds a subagent that inherits the parent's conversation
// and tools (except task, ask, goal_status, and workflow). Approvals
// bubble to the user unless the parent is already auto-approving.
func (t *Task) forkChild() (*Agent, error) {
	p := t.Parent
	var ts []tool.Tool
	for _, d := range p.Tools.Defs() {
		if d.Name == t.Name() {
			continue
		}
		tl, ok := p.Tools.Get(d.Name)
		if !ok {
			continue
		}
		if parentOnlyTool(tl) {
			continue
		}
		ts = append(ts, tl)
	}
	if len(ts) == 0 {
		return nil, fmt.Errorf("a fork has no tools left")
	}
	maxIter := t.MaxIterations
	if maxIter <= 0 {
		maxIter = 50
	}
	child := &Agent{
		Provider:      p.Provider,
		Tools:         tool.NewRegistry(ts...),
		System:        p.System + "\n\nYou are a fork of the parent conversation and inherited its history. Complete the brief. You cannot spawn another fork.",
		Model:         p.Model,
		MaxTokens:     p.MaxTokens,
		MaxIterations: maxIter,
		Messages:      copyMessages(p.Messages),
		AutoApprove:   p.AutoApprove,
		AutoEdits:     p.AutoEdits,
		AutoMode:      p.AutoMode,
		ReadOnly:      p.ReadOnly,
		PlanMode:      p.PlanMode,
		Rules:         p.Rules,
		CompactAt:     p.CompactAt,
		ContextWindow: p.ContextWindow,
		Archive:       p.Archive,
		Debug:         p.Debug,
		Forked:        true,
		Ledger:        p.Ledger,
	}
	if !p.AutoApprove && p.Approver != nil {
		child.Approver = bubbled{to: p.Approver, name: "fork"}
	}
	return child, nil
}

func copyMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, len(in))
	for i, m := range in {
		out[i] = m
		if m.Blocks == nil {
			continue
		}
		blocks := make([]llm.Block, len(m.Blocks))
		for j, b := range m.Blocks {
			blocks[j] = b
			if b.Input != nil {
				blocks[j].Input = append(json.RawMessage(nil), b.Input...)
			}
		}
		out[i].Blocks = blocks
	}
	return out
}

// usageSince is the usage added between two cumulative snapshots.
// briefInput is the one-line summary of a child tool call for the live
// activity readout.
func briefInput(raw string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return truncRunes(raw, 60)
	}
	for _, k := range []string{"command", "pattern", "path", "url", "description", "query"} {
		if v, ok := m[k].(string); ok {
			return truncRunes(v, 60)
		}
	}
	return truncRunes(raw, 60)
}

func truncRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max-1]) + "…"
}

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
