package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	agentprofile "github.com/xmonader/janetcode/internal/skill/profile"
	"github.com/xmonader/janetcode/internal/textfmt"
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
	Profiles func() []agentprofile.Profile
	// Root is the project directory; editing subagents check it out into
	// a git worktree and return their changes as a patch.
	Root string
	// Models, when set, resolves a model name to the provider that serves
	// it, so a job can be given to a different model than the one running
	// the session: a cheap one to read a lot of code, a strong one for
	// the part that has to be right. Without it the model parameter is
	// refused rather than silently ignored.
	Models func(model string) (llm.Provider, string, error)

	mu sync.Mutex
	// running counts the fork goroutines that outlive the tool call that
	// started them, so Close can wait for them rather than just cancelling.
	running  sync.WaitGroup
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
	// report is what a finished background fork has to say, held until
	// FinishedForks hands it to the parent turn. Empty once reported.
	report string
	// cancel stops this child's Run. Nil until the run has a context.
	cancel context.CancelFunc
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
		Last: c.last, Editing: c.wt != nil, Fork: c.fork, Usage: c.agent.UsageSnapshot(),
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
	// A fork is still running on its own goroutine: its history is taken
	// through the agent's own lock, not read from under it.
	return ChildLog{ChildInfo: childInfoLocked(id, c), Messages: c.agent.MessagesSnapshot()}, true
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
	d := "Delegate a self-contained job to a subagent that has its own context window: exploring an unfamiliar area of the codebase, tracing how something works, or gathering facts across many files. By default it can read, list, glob, grep and fetch URLs but cannot edit files or run commands; with edit: true it works in an isolated git worktree and returns its changes as a patch for you to apply. With fork: true it inherits this conversation's history, the same tools (except it cannot fork again), and runs in the background; continue it by id. Give it a complete brief with what to find and what to report back; it returns only its final report. Call several in one turn to work in parallel. Pass tools to restrict it to a few named tools when the job is narrow (fewer schemas, cheaper and more focused). Whether its mutating calls ask the user is not a call parameter: it comes from the agent profile's approval setting (approval: bubble in .janetcode/agents/<name>.md)."
	if ps := t.profiles(); len(ps) > 0 {
		var names []string
		for _, p := range ps {
			names = append(names, p.Name+" ("+p.Description+")")
		}
		d += " Custom agents, selectable with agent: " + strings.Join(names, "; ") + "."
	}
	return d
}

func (t *Task) profiles() []agentprofile.Profile {
	var user []agentprofile.Profile
	if t.Profiles != nil {
		user = t.Profiles()
	}
	return agentprofile.MergeProfiles(agentprofile.Builtins(), user)
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
"cancel":{"type":"string","description":"Optional: id of a running subagent to stop. The parent should use this when the user says to cancel that child."},
"steer":{"type":"string","description":"Optional: id of a running subagent to inject a note into, delivered before its next model call. prompt is the note."},
"fork":{"type":"boolean","description":"Optional: inherit this conversation's history and tools, run in the background, and return an id immediately. Cannot nest, and cannot be combined with edit."}},
"additionalProperties":false}`)
}

// ChildIDs lists continuable subagents started from this parent.
func (t *Task) ChildIDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.order...)
}

// Close cancels the forks still running, waits for them, and removes the
// worktrees of every editing subagent. Call it when the parent session
// ends. It is safe to call twice, and it never waits past ctx: a fork
// wedged in a provider call must not hold the process open.
func (t *Task) Close(ctx context.Context) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.stop != nil {
		t.stop()
		t.stop = nil
		t.life = nil
	}
	kids := t.children
	t.children, t.order = nil, nil
	t.mu.Unlock()
	t.Wait(ctx)
	for _, c := range kids {
		if c.wt != nil {
			c.wt.remove(ctx)
		}
	}
}

// Wait blocks until every background fork has returned, or until ctx is
// done. It reports whether they all finished.
func (t *Task) Wait(ctx context.Context) bool {
	if t == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.running.Wait()
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
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

// Cancel stops one running subagent. The parent model uses task
// cancel=<id>; the user uses /agents cancel <id>.
func (t *Task) Cancel(id string) error {
	if t == nil {
		return fmt.Errorf("no task tool")
	}
	// done and cancel are written by the goroutine running the child, so
	// both are read under the lock that goroutine takes.
	t.mu.Lock()
	kid := t.children[id]
	var (
		cancel context.CancelFunc
		done   bool
	)
	if kid != nil {
		cancel, done = kid.cancel, kid.done
	}
	t.mu.Unlock()
	if kid == nil {
		return fmt.Errorf("no subagent %q", id)
	}
	if done {
		return fmt.Errorf("subagent %s already finished", id)
	}
	if cancel == nil {
		return fmt.Errorf("subagent %s is not running", id)
	}
	cancel()
	return nil
}

// SteerChild injects a note into a running child before its next model
// call. This is mid-turn interjection, not a new continue turn.
func (t *Task) SteerChild(id, text string) error {
	if t == nil {
		return fmt.Errorf("no task tool")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("steer needs a prompt")
	}
	t.mu.Lock()
	kid := t.children[id]
	done := kid != nil && kid.done
	t.mu.Unlock()
	if kid == nil {
		return fmt.Errorf("no subagent %q", id)
	}
	if done {
		return fmt.Errorf("subagent %s already finished; use continue", id)
	}
	if kid.agent == nil {
		return fmt.Errorf("subagent %s has no agent", id)
	}
	kid.agent.Steer(text)
	return nil
}

func (t *Task) bindChildCtx(parent context.Context, kid *subagent, minutes int) context.Context {
	ctx, cancel := context.WithCancel(parent)
	if minutes > 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, time.Duration(minutes)*time.Minute)
		outer := cancel
		cancel = func() { timeoutCancel(); outer() }
	}
	t.mu.Lock()
	kid.cancel = cancel
	t.mu.Unlock()
	return ctx
}

func (t *Task) releaseChild(kid *subagent) {
	t.mu.Lock()
	cancel := kid.cancel
	kid.cancel = nil
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// remember registers a continuable subagent, evicting the oldest finished
// one (and its worktree) once there are too many.
//
// Only a finished child may be evicted. task is not a mutating tool, so
// several calls run in parallel goroutines and a background fork outlives
// the call that started it: evicting by age alone could remove the
// worktree an editing subagent was still working in, and drop the only
// handle by which a running fork could be cancelled. When every child is
// still running the list is left over its bound until one of them ends.
func (t *Task) remember(ctx context.Context, id string, c *subagent) {
	t.mu.Lock()
	if t.children == nil {
		t.children = map[string]*subagent{}
	}
	t.children[id] = c
	t.order = append(t.order, id)
	var evicted *subagent
	if len(t.order) > maxChildren {
		for i, old := range t.order {
			kid := t.children[old]
			// A finished fork whose report the parent has not collected
			// yet is kept too: FinishedForks reads it from here.
			if kid == nil || (kid.done && kid.report == "") {
				t.order = append(t.order[:i:i], t.order[i+1:]...)
				evicted = kid
				delete(t.children, old)
				break
			}
		}
	}
	t.mu.Unlock()
	if evicted != nil && evicted.wt != nil {
		// Cleanup, so it outlives a cancelled caller — but on the
		// caller's context all the same, and bounded: `git worktree
		// remove` on a checkout something else is holding can block for
		// as long as it likes, and this used to run on a
		// context.Background() with no deadline at all.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), evictTimeout)
		defer cancel()
		evicted.wt.remove(rctx)
	}
}

// evictTimeout bounds the git work that removing an evicted subagent's
// worktree does.
const evictTimeout = 2 * time.Minute

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
	if id := strings.TrimSpace(in.Cancel); id != "" {
		if err := t.Cancel(id); err != nil {
			return "", err
		}
		return "cancelled " + id, nil
	}
	if id := strings.TrimSpace(in.Steer); id != "" {
		if err := t.SteerChild(id, in.Prompt); err != nil {
			return "", err
		}
		return "steering " + id, nil
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
	var profile *agentprofile.Profile
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
		// Claimed here, under the same lock as the check: task calls run
		// in parallel, and a claim taken later (after the start hook,
		// which runs a command) let two continues of one finished child
		// both pass the check and drive the same agent at once — and let
		// a concurrent remember evict it, worktree and all, as finished
		// while this call was about to run it.
		if kid != nil && !running {
			kid.done = false
			kid.last = "starting"
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
		ag, cerr := t.editingChild(wt, profile)
		if cerr != nil {
			wt.remove(ctx)
			return "", cerr
		}
		kid = &subagent{agent: ag, wt: wt}
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
			model, prov := profile.Model, kid.agent.Settings().Provider
			if t.Models != nil {
				if p, resolved, err := t.Models(profile.Model); err == nil {
					prov, model = p, resolved
				}
			}
			kid.agent.UpdateSettings(func(s *Settings) { s.Provider, s.Model = prov, model })
		}
	}
	if routedProv != nil {
		kid.agent.UpdateSettings(func(s *Settings) { s.Provider, s.Model = routedProv, routedModel })
	}
	if childID == "" {
		t.mu.Lock()
		t.nextID++
		childID = fmt.Sprintf("task-%d", t.nextID)
		t.mu.Unlock()
		t.remember(ctx, childID, kid)
	}
	child, wt := kid.agent, kid.wt
	kind := subagentKind(kid, profile)
	in.Prompt += t.Parent.subagentStartHook(ctx, childID, kind)
	start := time.Now()
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
	minutes := 0
	if profile != nil {
		minutes = profile.MaxMinutes
	}
	if kid.fork && in.Continue == "" {
		// A fork must outlive this tool call: the parent turn continues
		// and the child is watched with /agents. The session context is
		// cancelled from Close, so quitting the TUI stops it. Cancel(id)
		// stops just this child.
		runCtx := t.bindChildCtx(t.sessionCtx(), kid, minutes)
		t.running.Add(1)
		desc := in.Description
		go func() {
			defer t.running.Done()
			defer t.releaseChild(kid)
			err := child.Run(runCtx, in.Prompt, emit)
			t.Parent.subagentStopHook(childID, kind, child)
			t.mu.Lock()
			kid.done = true
			kid.report = forkReport(childID, desc, time.Since(start), child, err)
			t.bill(kid, child)
			t.mu.Unlock()
		}()
		return fmt.Sprintf("started fork %q id=%s in the background; watch with /agents, continue with task continue=%s, cancel with task cancel=%s", in.Description, childID, childID, childID), nil
	}
	runCtx := t.bindChildCtx(ctx, kid, minutes)
	defer t.releaseChild(kid)
	err = child.Run(runCtx, in.Prompt, emit)
	t.Parent.subagentStopHook(childID, kind, child)
	t.mu.Lock()
	kid.done = true
	t.mu.Unlock()
	if err != nil && ctx.Err() == nil && runCtx.Err() != nil {
		t.mu.Lock()
		t.bill(kid, child)
		t.mu.Unlock()
		if runCtx.Err() == context.DeadlineExceeded && minutes > 0 {
			report := lastAssistantText(child.Messages)
			if strings.TrimSpace(report) == "" {
				report = "(the subagent ran out of time before reporting anything)"
			}
			return fmt.Sprintf("%s\n\n[subagent %q id=%s stopped after its %d-minute budget; continue it with task continue=%s]", report, in.Description, childID, minutes, childID), nil
		}
		return fmt.Sprintf("cancelled %s", childID), nil
	}
	// Usage is cumulative over the subagent's life, so only what this
	// exchange added is billed to the parent.
	t.mu.Lock()
	t.bill(kid, child)
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
	if cm := child.Settings().Model; cm != "" && cm != t.Parent.Settings().Model {
		on = " on " + cm
	}
	return fmt.Sprintf("%s\n\n[subagent %q id=%s%s: %d tool calls, %s, %s in / %s out; continue with task continue=%s]", report, in.Description, childID, on, countToolUses(child.Messages), time.Since(start).Round(time.Millisecond), tokens(child.Usage.InputTokens+child.Usage.CacheReadTokens), tokens(child.Usage.OutputTokens), childID), nil
}

// bill charges the parent for what this exchange with the child cost.
// The child's usage is cumulative over its life, so only what is new
// since the last look is added — to the task tool's own total, for
// /agents, and to the parent conversation, so the session total and the
// cost ceiling both see what delegation spent. Called with t.mu held.
func (t *Task) bill(kid *subagent, child *Agent) {
	delta := usageSince(kid.seen, child.Usage)
	kid.seen = child.Usage
	t.usage.Add(delta)
	t.Parent.addSideUsage(child.Settings().Model, delta)
}

// briefInput is the one-line summary of a child tool call for the live
// activity readout.
func briefInput(raw string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return textfmt.Clip(textfmt.OneLine(raw), 60)
	}
	for _, k := range []string{"command", "pattern", "path", "url", "description", "query"} {
		if v, ok := m[k].(string); ok {
			return textfmt.Clip(textfmt.OneLine(v), 60)
		}
	}
	return textfmt.Clip(textfmt.OneLine(raw), 60)
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

// forkReport is what the parent is told when a background fork ends. A
// fork edits the parent's own workspace and its work used to reach the
// parent only if someone ran /agents: a recorded session spawned two
// forks, replied, and the session log ended — the files they wrote
// appear nowhere in the transcript.
func forkReport(id, desc string, took time.Duration, child *Agent, err error) string {
	head := fmt.Sprintf("fork %q id=%s finished after %s", desc, id, took.Round(time.Second))
	switch {
	case errors.Is(err, context.Canceled):
		return head + " (cancelled)"
	case err != nil:
		return head + " with an error: " + err.Error()
	}
	body := strings.TrimSpace(lastAssistantText(child.Messages))
	if body == "" {
		body = "(it finished without a report)"
	}
	return head + "\n" + textfmt.Truncate(body)
}

// FinishedForks returns a note for every background fork that has ended
// since the last call, and never returns the same one twice.
func (t *Task) FinishedForks() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []string
	for _, id := range slices.Sorted(maps.Keys(t.children)) {
		kid := t.children[id]
		if kid.report == "" {
			continue
		}
		out = append(out, kid.report)
		kid.report = ""
	}
	return out
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

// SetRoot moves the project directory editing subagents check out from.
// Children already running keep the worktree they were given — it is a
// checkout of the tree they were asked about — so the count of those is
// reported rather than silently changed under them.
func (t *Task) SetRoot(dir string) (running int) {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Root = dir
	for _, c := range t.children {
		if !c.done {
			running++
		}
	}
	return running
}
