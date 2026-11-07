package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// An autocheck is the harness rerunning the check the model already ran,
// after the edits the model made since. On Kimi (effbench syn-kimi-pi) 53%
// of janetcode's tool calls were the model rerunning tests or an ad-hoc
// check script, and each round trip cost about 21 s; then_run, which asks
// the model to attach the command to its edit, was used 0 times in 126
// edits. The model does not opt in, so the command is learned from what it
// ran and the result is put where it would otherwise have asked for it.
// JANETCODE_OFF=autocheck turns it off.

const (
	// autoCheckSlow is the longest a check may have taken to be rerun on
	// every edit: past it the rerun costs more than the round trip it saves.
	autoCheckSlow = 60 * time.Second
	// autoCheckKeep is how many distinct check commands are rerun: the
	// tests and the model's own check file, which is what a run that checks
	// at all has. More would be every build and lint the turn once tried.
	autoCheckKeep = 2
)

// autoCheckOn reports whether the harness reruns checks for this agent.
func (a *Agent) autoCheckOn() bool {
	return a.Headless && !a.Off["autocheck"] && a.Log != nil && !a.DryRunOn()
}

// learnedChecks are the commands an autocheck would run: the model's check
// commands of this turn, each judged by its latest run exactly as the
// verdict ledger judges it (FoldOutcomeIn: a bash call that is a check, not
// refused, not a command that never started, not sent to the background),
// oldest first and at most autoCheckKeep. A command whose latest run took
// longer than autoCheckSlow is left out, and so is one that run went to the
// background: its result says nothing, and a rerun would hold the turn.
// Runs the harness made itself (an earlier autocheck, a then_run) are runs
// like any other, so a slow rerun retires the command too.
func learnedChecks(events []session.Event, turn int, root string) []string {
	type run struct {
		dur  time.Duration
		skip bool
	}
	latest := map[string]run{}
	var order []string
	for _, e := range events {
		if e.Type != session.TypeToolResult || e.Turn != turn || e.CallID == "" || e.Name != "bash" || refused(e) {
			continue
		}
		// The marker that forces a rerun past the shell cache makes no
		// second command: keyed with it, the same check was rerun twice.
		cmd := strings.TrimSpace(strings.ReplaceAll(shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), root), tool.NoCacheMarker, ""))
		if !shell.IsCheck(cmd) {
			continue
		}
		if _, ok := latest[cmd]; ok {
			order = slices.DeleteFunc(order, func(c string) bool { return c == cmd })
		}
		order = append(order, cmd)
		d := time.Duration(e.DurationMs) * time.Millisecond
		latest[cmd] = run{dur: d, skip: neverRan(e) || unfinished(e) || d > autoCheckSlow}
	}
	order = slices.DeleteFunc(order, func(c string) bool { return latest[c].skip })
	if len(order) > autoCheckKeep {
		order = order[len(order)-autoCheckKeep:]
	}
	return order
}

// autoCheck reruns the learned checks once a reply's calls have all run, if
// that reply edited a file and nothing in it checked the edit already, and
// appends what they printed to the last edit's result, so the model sees
// the outcome with no call of its own. The last edit's result and not the
// reply's last result: it is the change the output is about, and it is the
// result the model reads as "what happened to my edit". Each command runs
// as its own bash call (runBash), so each is an event the verdict counts as
// a check, with its own duration for the next autoCheckSlow judgement, and
// the run stops at the first one that fails, as `a && b` would: what
// the second says of a tree the first has just called broken is noise.
func (a *Agent) autoCheck(ctx context.Context, uses []llm.Block, results []llm.Block, emit func(Event)) error {
	if !a.autoCheckOn() {
		return nil
	}
	last := -1
	for i, u := range uses {
		if editTool(u.Name) && !results[i].IsError {
			last = i
		}
	}
	if last < 0 || replyChecked(uses, results, last) {
		return nil
	}
	if _, ok := a.Tools.Get("bash"); !ok {
		return nil
	}
	cmds := learnedChecks(a.Log.Events(), a.turnN, a.displayRoot())
	for i, cmd := range cmds {
		id := uses[last].ID + "~autocheck"
		if i > 0 {
			id += strconv.Itoa(i + 1)
		}
		res, isErr, err := a.runBash(ctx, id, cmd, emit)
		if err != nil {
			return err
		}
		results[last].Text += "\n\n[autocheck] $ " + checkLabel(cmd) + "\n" + res
		args, _ := json.Marshal(map[string]string{"command": cmd})
		if runFailed(session.Event{Output: res, IsError: isErr, Arguments: string(args)}) {
			break
		}
	}
	return nil
}

// replyChecked reports whether a check already ran after the reply's last
// edit: a then_run on that edit, or a later bash call in the same reply
// that is a check which actually ran. Rerunning it would show the model the
// same output twice.
func replyChecked(uses []llm.Block, results []llm.Block, last int) bool {
	if cmd, _ := tool.TakeThenRun(uses[last].Input); cmd != "" && shell.IsCheck(cmd) && strings.Contains(results[last].Text, "\n\n[then_run] $ ") {
		return true
	}
	for j := last + 1; j < len(uses); j++ {
		if uses[j].Name != "bash" {
			continue
		}
		e := session.Event{Output: results[j].Text, IsError: results[j].IsError}
		if shell.IsCheck(shell.CommandOf(string(uses[j].Input))) && !refused(e) && !neverRan(e) && !unfinished(e) {
			return true
		}
	}
	return false
}
