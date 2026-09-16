// Package run replays a saved recipe's steps. It is the part of
// "/recipe run" that has nothing to do with a conversation: walk the
// steps, check each precondition, hand the call to whatever actually
// executes tool calls, and build the report.
//
// The conversation half — opening and closing a logged turn around the
// replay, the project's verify gate, compaction, the diff summary —
// stays in internal/agent, which owns the turn. That package drives this
// one through Dispatcher, and this one never imports it.
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Dispatcher runs one tool call through everything a call the model made
// goes through: permission rules, approvals, guards, checkpoints,
// provenance and the event log. *agent.Agent is the implementation
// (runNested); the interface is here so the replay loop can be driven
// without a conversation behind it.
type Dispatcher interface {
	Execute(ctx context.Context, id, name string, input json.RawMessage) (out string, isErr, denied bool)
}

// Failure is the step a recipe stopped on: enough for the model to
// pick up where it left off (see Handover), and for a front-end to
// say what happened. A failure of the project's verify gate, run after
// every step has already succeeded, is reported the same way with Step
// left at its zero value, Tool "verify" and Verify set, rather than as a
// new kind of result the front-ends would need a second case for.
type Failure struct {
	Step   int
	Tool   string
	Output string
	// Verify marks a failure of the project's verify gate rather than of
	// a step: every step succeeded, but the checks that ran afterwards
	// did not.
	Verify bool
	// Denied marks a failure that was the harness itself refusing the
	// call — a permission rule, a hook block, a "no" from the approver,
	// read-only mode, or a failed Expect precondition — rather than the
	// tool running and failing on its own. A front-end must not hand a
	// denial to the model: the model's own retry would be refused by the
	// very same rule or precondition, so the only effect of handing it
	// over is spending a turn to learn nothing. Report it instead (see
	// cmd/janetcode's runRecipeFlag and the tui/webui recipe commands).
	Denied bool
}

// mcpToolPrefix names a tool an MCP server contributed to the registry:
// internal/mcp's Client.AgentTools registers every one of a server's
// tools (and its resource helpers) through ToolName as
// "mcp__<server>__<tool>". It is the only marker a recipe step carries
// that says an MCP server has to be running for it to work.
const mcpToolPrefix = "mcp__"

// NeedsMCP reports whether any step of r calls a tool an MCP server
// provides. A caller building the agent for `janetcode --recipe` uses
// this to decide whether starting configured MCP servers — each its own
// process, handshake and tool listing — is worth the time at all: most
// recipes are plain file and shell steps and never need one.
func NeedsMCP(r recipe.Recipe) bool {
	for _, st := range r.Steps {
		if strings.HasPrefix(st.Tool, mcpToolPrefix) {
			return true
		}
	}
	return false
}

// Label is the synthetic user message a recipe run's turn carries,
// so a session read back later shows what happened. session.Synthetic
// treats it as machinery, the same as an undo or a goal round.
// The arguments are sorted, so the same run always writes the same label
// into the session: a map's order is randomised, and a label that shifts
// between runs makes two identical replays look different in the log and
// in any diff of two sessions.
func Label(name string, args map[string]string) string {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := "/recipe run " + name
	for _, k := range keys {
		s += " " + k + "=" + args[k]
	}
	return s
}

// Steps replays steps through d, in order, stopping at the first one that
// fails or is refused. It reports the run's report text so far, the call
// ids it handed out (so the caller can attribute the run's own changes),
// and the step it stopped on. A non-nil error is ctx's: the replay was
// cancelled, and the caller's turn ends rather than finishing.
//
// tools resolves a step's tool for its precondition check (see Expect);
// log records what a step's refusal would otherwise leave no trace of.
func Steps(ctx context.Context, d Dispatcher, tools *tool.Registry, name string, steps []recipe.Step, log func(session.Event)) (report string, ids map[string]bool, failed *Failure, err error) {
	var b strings.Builder
	fmt.Fprintf(&b, "recipe %s (%s)\n", name, textfmt.Plural(len(steps), "step"))
	ids = make(map[string]bool, len(steps))
	for i, st := range steps {
		if err := ctx.Err(); err != nil {
			return strings.TrimRight(b.String(), "\n"), ids, nil, err
		}
		// A step's precondition (see recipe.Expect) is checked before it
		// ever reaches the dispatcher: the whole point of Expect is that
		// this step must not run at all when what it is about to touch is
		// not what the recording verified, so there is nothing for
		// authorize or the tool itself to weigh in on here.
		if msg := CheckExpect(tools, st); msg != "" {
			fmt.Fprintf(&b, "## step %d: %s\n%s\n", i+1, st.Tool, msg)
			log(session.Event{Type: session.TypeToolResult, Name: st.Tool, Output: msg, IsError: true})
			return b.String(), ids, &Failure{Step: i + 1, Tool: st.Tool, Output: msg, Denied: true}, nil
		}
		id := fmt.Sprintf("recipe-%s-%d", name, i+1)
		ids[id] = true
		out, isErr, denied := d.Execute(ctx, id, st.Tool, st.Input)
		fmt.Fprintf(&b, "## step %d: %s\n%s\n", i+1, st.Tool, strings.TrimRight(out, "\n"))
		if isErr {
			return b.String(), ids, &Failure{Step: i + 1, Tool: st.Tool, Output: out, Denied: denied}, nil
		}
	}
	return b.String(), ids, nil, nil
}

// CheckExpect reports why st must not run, or "" when it may proceed. Only
// steps with an Expect precondition (see recipe.Expect) are checked; every
// other step returns "" here; and the actual pre-existing check (a rule,
// a hook, the approver) still runs afterwards, from the dispatcher,
// exactly as for any other call.
func CheckExpect(tools *tool.Registry, st recipe.Step) string {
	if st.Expect == nil || !st.Expect.Absent || tools == nil {
		return ""
	}
	t, ok := tools.Get(st.Tool)
	if !ok {
		return "" // an unknown tool is reported by the dispatcher, same as ever
	}
	tc, ok := t.(tool.Toucher)
	if !ok {
		return ""
	}
	paths, err := tc.Touches(st.Input)
	if err != nil || len(paths) == 0 {
		return ""
	}
	var in struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal(st.Input, &in)
	data, rerr := os.ReadFile(paths[0])
	switch {
	case errors.Is(rerr, os.ErrNotExist):
		return "" // absent, exactly as it was when this was recorded
	case rerr != nil:
		return fmt.Sprintf("could not check %s before writing it: %v", paths[0], rerr)
	case string(data) == in.Content:
		return "" // already holds exactly what this step would write
	default:
		return fmt.Sprintf("%s did not exist when this recipe was recorded, but it exists now with different content; refusing to overwrite it rather than guess. Check it by hand, or update the recipe.", paths[0])
	}
}

// Handover is the prompt given to the model after a recipe failed —
// either a step, or the project's verify gate once every step had
// succeeded: which is which, its output, which steps already succeeded,
// and the recipe's own description as the goal to finish. A front-end
// starts an ordinary turn with it, so everything downstream — approvals,
// verify, verdicts — behaves exactly as it would for anything else the
// user asked for.
func Handover(r recipe.Recipe, f Failure) string {
	var b strings.Builder
	if f.Verify {
		fmt.Fprintf(&b, "The recipe %q ran all %s and every one of them succeeded, but the project's checks failed afterwards.\n", r.Name, textfmt.Plural(len(r.Steps), "step"))
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(f.Output))
		if d := strings.TrimSpace(r.Description); d != "" {
			fmt.Fprintf(&b, "\nWhat this recipe is for: %s\n", d)
		}
		b.WriteString("\nFix it, then finish the job yourself, using whatever tools and judgement it takes.")
		return b.String()
	}
	fmt.Fprintf(&b, "The recipe %q stopped at step %d of %d: %s failed.\n", r.Name, f.Step, len(r.Steps), f.Tool)
	if f.Step > 1 {
		names := make([]string, 0, f.Step-1)
		for _, st := range r.Steps[:f.Step-1] {
			names = append(names, st.Tool)
		}
		fmt.Fprintf(&b, "Already ran and succeeded: %s.\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, "\n%s's output:\n%s\n", f.Tool, strings.TrimSpace(f.Output))
	if d := strings.TrimSpace(r.Description); d != "" {
		fmt.Fprintf(&b, "\nWhat this recipe is for: %s\n", d)
	}
	b.WriteString("\nWork out why this step failed and finish the job yourself, using whatever tools and judgement it takes.")
	return b.String()
}
