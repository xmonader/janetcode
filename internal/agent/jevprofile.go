package agent

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// Phase is what a call was for: finding things out, changing the tree,
// checking the tree, or none of those.
type Phase string

// The four phases a call is classified into.
const (
	PhaseExplore Phase = "explore"
	PhaseEdit    Phase = "edit"
	PhaseVerify  Phase = "verify"
	PhaseOther   Phase = "other"
)

// Phases in report order.
var Phases = []Phase{PhaseExplore, PhaseEdit, PhaseVerify, PhaseOther}

// exploreTools name themselves: every one of them only reads.
var exploreTools = map[string]bool{
	"read_file":       true,
	"read_many_files": true,
	"list_dir":        true,
	"glob":            true,
	"grep":            true,
	"repo_map":        true,
	"expand_output":   true,
	"tool_search":     true,
	"web_search":      true,
	"fetch_url":       true,
	"introspect":      true,
}

// editTools change the tree directly.
var editTools = map[string]bool{
	"write_file": true,
	"edit_file":  true,
}

// readingShellHeads are command heads that only look at the tree. Anything
// else that is not a check is left in PhaseOther rather than guessed at.
var readingShellHeads = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true,
	"rg": true, "find": true, "wc": true, "which": true, "pwd": true,
	"file": true, "stat": true, "tree": true, "sed": true, "awk": true,
	"diff": true, "du": true, "env": true, "echo": true,
}

// writingShellHeads change the tree without going through a tool.
var writingShellHeads = map[string]bool{
	"mkdir": true, "rm": true, "mv": true, "cp": true, "touch": true,
	"gofmt": true, "chmod": true, "tee": true, "patch": true, "ln": true,
}

// ClassifyCall says which phase a tool call belongs to, from its name and
// its arguments alone. It is a pure function of what a session log records,
// so the live harness and an offline replay cannot disagree about it.
func ClassifyCall(name, arguments string) Phase {
	switch {
	case exploreTools[name]:
		return PhaseExplore
	case editTools[name]:
		return PhaseEdit
	case name == "bash" || name == "execute_code":
		return classifyCommand(name, arguments)
	default:
		return PhaseOther
	}
}

func classifyCommand(name, arguments string) Phase {
	if name == "execute_code" {
		// The scripting tool reads and writes through the same helpers; the
		// honest answer without parsing Python is "other".
		return PhaseOther
	}
	cmd := shell.CommandOf(arguments)
	if cmd == "" {
		return PhaseOther
	}
	if shell.IsCheck(cmd) {
		return PhaseVerify
	}
	head := commandHead(cmd)
	switch {
	case writingShellHeads[head]:
		return PhaseEdit
	case readingShellHeads[head]:
		return PhaseExplore
	case head == "git":
		if rest := afterHead(cmd); strings.HasPrefix(rest, "status") ||
			strings.HasPrefix(rest, "diff") || strings.HasPrefix(rest, "log") ||
			strings.HasPrefix(rest, "show") || strings.HasPrefix(rest, "ls-files") {
			return PhaseExplore
		}
		return PhaseOther
	default:
		return PhaseOther
	}
}

// commandHead is the first word of the first pipeline segment, with any
// leading environment assignments dropped.
func commandHead(cmd string) string {
	for _, f := range strings.Fields(firstSegment(cmd)) {
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "-") {
			continue
		}
		if i := strings.LastIndex(f, "/"); i >= 0 {
			f = f[i+1:]
		}
		return f
	}
	return ""
}

func afterHead(cmd string) string {
	fields := strings.Fields(firstSegment(cmd))
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

func firstSegment(cmd string) string {
	for i := 0; i < len(cmd); i++ {
		switch cmd[i] {
		case '|', ';', '&', '\n':
			return cmd[:i]
		}
	}
	return cmd
}

// CallProfile is what one recorded run spent its calls and its input tokens
// on. Every field is counted from the log, never estimated by a model.
type CallProfile struct {
	Model string
	Runs  int

	ModelCalls int
	ToolCalls  int

	// ToolPhase and ModelPhase count tool calls, and model calls, per phase.
	// A model call takes the phase of the calls it issued: explore if it
	// issued only explore calls, edit if it issued any edit call, and so on
	// (edit > verify > explore > other), and PhaseOther when it issued none.
	ToolPhase  map[Phase]int
	ModelPhase map[Phase]int

	// CallsBeforeFirstEdit is summed over runs that reached an edit;
	// EditingRuns counts those runs, so the mean is the ratio.
	CallsBeforeFirstEdit int
	EditingRuns          int

	// InputFirst and InputLast sum the first and last model call's input
	// (fresh plus cache-read) over runs; InputGrowth sums
	// (last-first)/(calls-1) over runs with at least two calls, and
	// GrowthRuns counts them.
	InputFirst, InputLast int
	InputGrowth           float64
	GrowthRuns            int

	// InputTokens is every model call's input, fresh plus cache-read: the
	// bytes the provider actually read, which is what a context cut saves.
	InputTokens int

	// ToolOutputPaid is the tool-result tokens summed over every later model
	// call that still carried them — the share of InputTokens that is tool
	// output. ToolOutputStale is the part of that which is older than N
	// steps and never referenced again.
	ToolOutputPaid, ToolOutputStale int

	Results, StaleResults int

	// Compactions counts how often the run crossed the compaction boundary:
	// the only place item 3 is allowed to act.
	Compactions int

	// Waste counts tool calls under each by-construction waste label.
	Waste map[string]int

	// Elide is the round-trip elision pre-screen (elidescreen.go).
	Elide ElideScreen

	// Points is one row per run, so a report can put a spread on any of
	// these rates instead of quoting a pooled number as if it were exact.
	Points []RunPoint
}

// RunPoint is one run's totals: the unit a bootstrap resamples.
type RunPoint struct {
	Passed          bool
	ModelCalls      int
	ToolCalls       int
	Explore         int
	Edit            int
	Verify          int
	Wasted          int
	BeforeFirstEdit int
	InputTokens     int
	OutputTokens    int
}

func newCallProfile(model string) *CallProfile {
	return &CallProfile{
		Model:      model,
		ToolPhase:  map[Phase]int{},
		ModelPhase: map[Phase]int{},
		Waste:      map[string]int{},
	}
}

// Add folds another run's profile into this one.
func (p *CallProfile) Add(o *CallProfile) {
	p.Runs += o.Runs
	p.ModelCalls += o.ModelCalls
	p.ToolCalls += o.ToolCalls
	for _, ph := range Phases {
		p.ToolPhase[ph] += o.ToolPhase[ph]
		p.ModelPhase[ph] += o.ModelPhase[ph]
	}
	p.CallsBeforeFirstEdit += o.CallsBeforeFirstEdit
	p.EditingRuns += o.EditingRuns
	p.InputFirst += o.InputFirst
	p.InputLast += o.InputLast
	p.InputGrowth += o.InputGrowth
	p.GrowthRuns += o.GrowthRuns
	p.InputTokens += o.InputTokens
	p.ToolOutputPaid += o.ToolOutputPaid
	p.ToolOutputStale += o.ToolOutputStale
	p.Results += o.Results
	p.StaleResults += o.StaleResults
	p.Compactions += o.Compactions
	for k, v := range o.Waste {
		p.Waste[k] += v
	}
	p.Elide.Add(o.Elide)
	p.Points = append(p.Points, o.Points...)
}

// ProfileTokens is the token estimate the profiler uses for tool output.
// Provider usage numbers cover a whole request, not one block, so the share
// of a request that is one tool result has to be estimated; four bytes per
// token is the ratio this repository already uses in EstimateTokens.
func ProfileTokens(s string) int { return len(s) / 4 }

// staleQuoteMin is how long a line of tool output has to be before a later
// mention of it counts as a quote rather than a coincidence.
const staleQuoteMin = 24

// ProfileRun classifies one recorded run. staleAfter is the age in model
// calls past which an unreferenced tool result counts as dead weight.
func ProfileRun(model string, events []session.Event, staleAfter int, passed bool) *CallProfile {
	p := newCallProfile(model)
	p.Runs = 1
	waste := WasteLabels(events, passed)
	wasteAt := 0
	point := RunPoint{Passed: passed}

	type call struct {
		name, args string
		modelCall  int
		phase      Phase
	}
	calls := map[string]*call{}

	type result struct {
		subject   string
		tokens    int
		lines     []string
		modelCall int
	}
	var results []result

	// laterText collects, per model-call index, the assistant text written at
	// or after it, so a quote can be looked for without rescanning.
	var assistantText []string
	var seenEdit bool
	modelCalls := 0
	phaseOf := map[int]Phase{}

	for _, e := range events {
		switch e.Type {
		case session.TypeAssistant:
			modelCalls++
			assistantText = append(assistantText, messageText(e.Message))
			if e.Usage != nil {
				in := e.Usage.InputTokens + e.Usage.CacheReadTokens
				p.InputTokens += in
				point.OutputTokens += e.Usage.OutputTokens
				if modelCalls == 1 {
					p.InputFirst = in
				}
				p.InputLast = in
			}
		case session.TypeToolCall:
			ph := ClassifyCall(e.Name, e.Arguments)
			p.ToolCalls++
			p.ToolPhase[ph]++
			if wasteAt < len(waste) {
				if lbl := waste[wasteAt]; lbl != WasteNone {
					p.Waste[lbl]++
					point.Wasted++
				}
				wasteAt++
			}
			if ph == PhaseEdit && !seenEdit {
				seenEdit = true
				p.CallsBeforeFirstEdit = modelCalls
				p.EditingRuns = 1
			}
			if old, ok := phaseOf[modelCalls]; !ok || phaseRank(ph) > phaseRank(old) {
				phaseOf[modelCalls] = ph
			}
			calls[e.CallID] = &call{name: e.Name, args: e.Arguments, modelCall: modelCalls, phase: ph}
		case session.TypeToolResult:
			c := calls[e.CallID]
			if c == nil || e.Output == "" {
				continue
			}
			results = append(results, result{
				subject:   callSubject(c.name, c.args),
				tokens:    ProfileTokens(e.Output),
				lines:     longLines(e.Output),
				modelCall: c.modelCall,
			})
		case session.TypeCompactEnd:
			p.Compactions++
		}
	}
	p.ModelCalls = modelCalls
	for i := 1; i <= modelCalls; i++ {
		ph, ok := phaseOf[i]
		if !ok {
			ph = PhaseOther
		}
		p.ModelPhase[ph]++
	}
	if modelCalls >= 2 && p.InputFirst > 0 {
		p.InputGrowth = float64(p.InputLast-p.InputFirst) / float64(modelCalls-1)
		p.GrowthRuns = 1
	}

	// Later subjects, by the model call at which they are next touched.
	subjectTouch := map[string][]int{}
	for _, c := range calls {
		if s := callSubject(c.name, c.args); s != "" {
			subjectTouch[s] = append(subjectTouch[s], c.modelCall)
		}
	}
	for _, v := range subjectTouch {
		sort.Ints(v)
	}

	point.ModelCalls = p.ModelCalls
	point.ToolCalls = p.ToolCalls
	point.Explore = p.ModelPhase[PhaseExplore]
	point.Edit = p.ModelPhase[PhaseEdit]
	point.Verify = p.ModelPhase[PhaseVerify]
	point.BeforeFirstEdit = p.CallsBeforeFirstEdit
	point.InputTokens = p.InputTokens
	p.Points = []RunPoint{point}
	p.Elide = ScreenElision(events)

	for _, r := range results {
		p.Results++
		carried := modelCalls - r.modelCall
		if carried < 0 {
			carried = 0
		}
		p.ToolOutputPaid += r.tokens * carried
		if referencedAfter(r.subject, r.lines, r.modelCall, subjectTouch, assistantText) {
			continue
		}
		p.StaleResults++
		dead := modelCalls - r.modelCall - staleAfter
		if dead > 0 {
			p.ToolOutputStale += r.tokens * dead
		}
	}
	return p
}

func phaseRank(p Phase) int {
	switch p {
	case PhaseEdit:
		return 3
	case PhaseVerify:
		return 2
	case PhaseExplore:
		return 1
	}
	return 0
}

// referencedAfter is the by-construction test: the subject is touched again
// by a later call, or a long line of the output is quoted in later
// assistant text.
func referencedAfter(subject string, lines []string, at int, touch map[string][]int, assistantText []string) bool {
	if subject != "" {
		for _, n := range touch[subject] {
			if n > at {
				return true
			}
		}
	}
	if len(lines) == 0 {
		return false
	}
	for i := at; i < len(assistantText); i++ {
		t := assistantText[i]
		if t == "" {
			continue
		}
		for _, ln := range lines {
			if strings.Contains(t, ln) {
				return true
			}
		}
	}
	return false
}

// callSubject is the path, pattern or command a call is about — the same
// notion lastReference uses when pruning.
func callSubject(name, arguments string) string {
	var a struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
		Command string `json:"command"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return ""
	}
	switch {
	case a.Path != "":
		return name + ":" + a.Path
	case a.Pattern != "":
		return name + ":" + a.Pattern
	case a.Command != "":
		return name + ":" + strings.TrimSpace(a.Command)
	}
	return ""
}

// longLines are the lines of an output long enough that finding one again in
// assistant text means the model read this result, capped so a huge output
// does not dominate the scan.
func longLines(out string) []string {
	var out2 []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if len(ln) < staleQuoteMin {
			continue
		}
		out2 = append(out2, ln)
		if len(out2) >= 40 {
			break
		}
	}
	return out2
}

func messageText(m *llm.Message) string {
	if m == nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range m.Blocks {
		if bl.Type == llm.BlockText || bl.Type == llm.BlockThinking {
			b.WriteString(bl.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
