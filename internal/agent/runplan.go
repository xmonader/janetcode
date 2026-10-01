package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// RunPlan is the compiled half of the explore-then-compile loop
// (docs/benchmarks/jit): once the model knows what to change, it hands
// over the rest of the work as one program — edits, then the checks that
// prove them — and the harness runs it without a model call between
// steps. Reads with no unfinished dependency run in parallel; anything
// that changes something runs alone, in the order written. The first
// step that fails, or the first check that exits non-zero, stops the
// program ("deoptimises" it): the model gets back that step's whole
// output, one line per step that succeeded, and the steps never run,
// and decides from there. A successful edit costs one line of context
// instead of a model round trip.
//
// Every step is a real tool call through runNested: the same permission
// rules, approvals, hooks, snapshots and event log as a call the model
// makes itself.
type RunPlan struct{ Parent *Agent }

const runPlanMaxSteps = 40

// checkTail is how much of a passing check's output comes back: enough
// to see the summary line a test runner prints, not the whole log.
const checkTail = 12

func (*RunPlan) Name() string   { return "run_plan" }
func (*RunPlan) Mutating() bool { return true }
func (*RunPlan) Description() string {
	return "Run the rest of a task as one program once you know what to change: every edit, then the commands that check it. " +
		"steps is a list of {id, tool, input, after?, check?, show?}; each step is a real tool call (same permissions and approvals). " +
		"Steps run in the order written; reads whose `after` steps are done run in parallel. " +
		"A step with check: true is a verification command (build, test, lint): if it exits non-zero the program stops there. " +
		"Any failing step stops the program too. You get back one line per finished step, the full output of the step that failed, and the steps that never ran — fix from that point, usually with another run_plan. " +
		"Successful edits are reported as one line; set show: true on a step whose output you need to read. Inputs are fixed when you write the plan: a step cannot use an earlier step's output, so explore first, then plan."
}
func (*RunPlan) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"steps":{"type":"array","items":{"type":"object","properties":{
"id":{"type":"string","description":"short unique name, e.g. e1, test"},
"tool":{"type":"string","description":"tool to call, e.g. edit_file, write_file, bash"},
"input":{"type":"object","description":"that tool's arguments"},
"after":{"type":"array","items":{"type":"string"},"description":"ids of earlier steps this one must wait for (only matters for reads; writes always run in order)"},
"check":{"type":"boolean","description":"a verification command: a non-zero exit stops the program"},
"show":{"type":"boolean","description":"return this step's full output even when it succeeds"}},
"required":["tool","input"]}},
"goal":{"type":"string","description":"one line: what the program does"}},
"required":["steps"],"additionalProperties":false}`)
}

type planStep struct {
	ID    string          `json:"id"`
	Tool  string          `json:"tool"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	After []string        `json:"after"`
	Check bool            `json:"check"`
	Show  bool            `json:"show"`
}

type planResult struct {
	out     string
	failed  bool
	ran     bool
	reading bool
}

var exitCodeRe = regexp.MustCompile(`(?m)^\[exit code (\d+)\]`)

// checkFailed reports whether a check step's output ended in a non-zero
// exit. bash reports a non-zero exit in the text ("[exit code N]"), not as
// a tool error, because exit 1 is often an answer; for a step the model
// marked as a check it is the failure that stops the program.
func checkFailed(out string) bool {
	m := exitCodeRe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return false
	}
	n, err := strconv.Atoi(m[len(m)-1][1])
	return err == nil && n != 0
}

func parsePlan(raw json.RawMessage) ([]planStep, error) {
	var in struct {
		Steps json.RawMessage `json:"steps"`
		Goal  string          `json:"goal"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("bad input: %w", err)
	}
	var steps []planStep
	if err := decodeSteps(in.Steps, "steps", "{id, tool, input}", &steps); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, fmt.Errorf("steps must contain at least one step")
	}
	if len(steps) > runPlanMaxSteps {
		return nil, fmt.Errorf("at most %d steps; split the work into two plans", runPlanMaxSteps)
	}
	seen := map[string]bool{}
	for i := range steps {
		s := &steps[i]
		if s.Tool == "" {
			s.Tool = s.Name
		}
		s.Tool = strings.TrimSpace(s.Tool)
		if s.Tool == "" {
			return nil, fmt.Errorf("step %d: tool is required", i+1)
		}
		if runCodeSkip[s.Tool] || s.Tool == "run_plan" {
			return nil, fmt.Errorf("step %d: %s cannot be called from run_plan", i+1, s.Tool)
		}
		if s.ID == "" {
			s.ID = fmt.Sprintf("s%d", i+1)
		}
		if seen[s.ID] {
			return nil, fmt.Errorf("step %d: id %q is used twice", i+1, s.ID)
		}
		for _, d := range s.After {
			if !seen[d] {
				return nil, fmt.Errorf("step %s: after %q names no earlier step", s.ID, d)
			}
		}
		seen[s.ID] = true
		if len(s.Input) == 0 {
			s.Input = json.RawMessage(`{}`)
		}
	}
	return steps, nil
}

func (t *RunPlan) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if t.Parent == nil {
		return "", fmt.Errorf("run_plan has no parent agent")
	}
	steps, err := parsePlan(input)
	if err != nil {
		return "", err
	}
	res := make([]planResult, len(steps))
	index := map[string]int{}
	for i, s := range steps {
		index[s.ID] = i
	}
	reads := func(s planStep) bool {
		tl, ok := t.Parent.Tools.Get(s.Tool)
		return ok && !s.Check && !tool.MutatingCall(tl, s.Input)
	}
	run := func(i int) {
		s := steps[i]
		out, isErr, _ := t.Parent.runNested(ctx, "plan-"+s.ID, s.Tool, s.Input)
		res[i] = planResult{out: out, ran: true, failed: isErr || (s.Check && checkFailed(out)), reading: reads(s)}
	}

	stop := -1
	for i := 0; i < len(steps) && stop < 0; {
		if ctx.Err() != nil {
			break
		}
		if !reads(steps[i]) {
			run(i)
			if res[i].failed {
				stop = i
			}
			i++
			continue
		}
		// A batch of reads: this one and every following read whose
		// dependencies are all outside the batch (already done).
		j := i
		inBatch := map[string]bool{}
		for j < len(steps) && reads(steps[j]) {
			dep := false
			for _, d := range steps[j].After {
				if inBatch[d] {
					dep = true
				}
			}
			if dep {
				break
			}
			inBatch[steps[j].ID] = true
			j++
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, MaxParallelTools)
		for k := i; k < j; k++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				run(k)
			}(k)
		}
		wg.Wait()
		for k := i; k < j; k++ {
			if res[k].failed {
				stop = k
				break
			}
		}
		i = j
	}
	return renderPlan(steps, res, stop), nil
}

// renderPlan is what the model reads back. It is built to be short when
// everything worked — the point of a plan is that its intermediate
// results do not need to enter the context — and complete where it
// stopped, since that is the one place the model has to think.
func renderPlan(steps []planStep, res []planResult, stop int) string {
	var b strings.Builder
	done := 0
	for i, s := range steps {
		r := res[i]
		if !r.ran {
			continue
		}
		done++
		switch {
		case i == stop:
			fmt.Fprintf(&b, "✗ %s %s — stopped here:\n%s\n", s.ID, s.Tool, textfmt.TruncateTo(strings.TrimRight(r.out, "\n"), 12000))
		case s.Show || r.reading:
			fmt.Fprintf(&b, "✓ %s %s:\n%s\n", s.ID, s.Tool, strings.TrimRight(r.out, "\n"))
		case s.Check:
			fmt.Fprintf(&b, "✓ %s %s (check passed):\n%s\n", s.ID, s.Tool, lastLines(r.out, checkTail))
		default:
			fmt.Fprintf(&b, "✓ %s %s: %s\n", s.ID, s.Tool, firstLine(r.out))
		}
	}
	var skipped []string
	for i, s := range steps {
		if !res[i].ran {
			skipped = append(skipped, s.ID)
		}
	}
	switch {
	case stop >= 0:
		fmt.Fprintf(&b, "\n[plan stopped at %s after %s; not run: %s]", steps[stop].ID, textfmt.Plural(done, "step"), strings.Join(orNone(skipped), ", "))
	case len(skipped) > 0:
		fmt.Fprintf(&b, "\n[plan cancelled after %s; not run: %s]", textfmt.Plural(done, "step"), strings.Join(skipped, ", "))
	default:
		fmt.Fprintf(&b, "\n[plan finished: %s, all succeeded]", textfmt.Plural(done, "step"))
	}
	return strings.TrimLeft(b.String(), "\n")
}

func orNone(s []string) []string {
	if len(s) == 0 {
		return []string{"none"}
	}
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "ok"
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{"…"}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

var _ tool.Tool = (*RunPlan)(nil)
