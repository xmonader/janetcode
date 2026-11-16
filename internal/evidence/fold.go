package evidence

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// GateExtra is the Extra a turn-end gate writes on its session.TypeGate
// event, one per command it ran or skipped. Exit and the output digest
// are what signed evidence records and `--proof verify` compares a
// re-run against. A gate event without it, written before the evidence
// model existed, is read as: IsError means failed, anything else passed.
type GateExtra struct {
	Exit          int    `json:"exit"`
	Output        string `json:"out,omitempty"`
	OutputPartial bool   `json:"partial,omitempty"`
	// Status is the run's evidence status (Classify, or NotRun for a
	// command the gate never reached, Cancelled for an interrupted one).
	Status string `json:"status,omitempty"`
	// Summary is the one line of output worth reading.
	Summary string `json:"summary,omitempty"`
}

// Options says how to read the log.
type Options struct {
	// Turn limits the checks to one turn's (and the edits that need
	// evidence to that turn's, up to its end). Zero reads the session.
	Turn int
	// Root is the workspace root: a leading `cd <root> &&` is not part
	// of a command, and file paths are shown relative to it.
	Root string
	// Exempt reports a file whose edit needs no evidence (documentation).
	// Such an edit neither requires a check nor makes one stale.
	Exempt func(path string) bool
	// Refusals are the Output prefixes of a bash call the harness
	// refused without running it; such a call is no check at all.
	Refusals []string
	// Interrupted are the Output prefixes of a call the user cancelled.
	Interrupted []string
}

var (
	exitLineRe = regexp.MustCompile(`\[exit code (-?\d+)\]`)
	notFoundRe = regexp.MustCompile(`(?im)(command not found|: not found)\s*$`)
)

// editToolNames are the file tools: a log from before file/change
// events existed says it edited only through them.
var editToolNames = map[string]bool{"write_file": true, "edit_file": true, "notebook_edit": true, "ast_edit": true}

type edit struct {
	path   string
	idx    int
	turn   int
	exempt bool
}

type run struct {
	Check
	epoch int // how many edits had been made when it ran
}

// maxFiles bounds the files listed on one check.
const maxFiles = 20

// FromEvents folds a session log into its evidence Report. It does no
// I/O and reads only what the harness recorded: gate events, the model's
// own check commands, file changes and the decision service's advisories.
func FromEvents(events []session.Event, o Options) Report {
	exempt := func(p string) bool { return o.Exempt != nil && o.Exempt(p) }
	rel := func(p string) string {
		if o.Root != "" && strings.HasPrefix(p, o.Root+"/") {
			return p[len(o.Root)+1:]
		}
		return p
	}
	changed := map[string]bool{} // call ids with a file/change event
	for _, e := range events {
		if e.Type == session.TypeFileChange && e.CallID != "" {
			changed[e.CallID] = true
		}
	}
	var edits []edit
	var runs []run
	type advice struct {
		idx  int
		turn int
		a    Advisory
	}
	var advices []advice
	for i, e := range events {
		switch e.Type {
		case session.TypeFileChange:
			edits = append(edits, edit{path: e.Name, idx: i, turn: e.Turn, exempt: exempt(e.Name)})
		case session.TypeGate:
			runs = append(runs, run{Check: gateCheck(e, i, o.Root), epoch: len(edits)})
		case session.TypeDecision:
			if a, ok := adviceOf(e); ok {
				advices = append(advices, advice{i, e.Turn, a})
			}
		case session.TypeToolResult:
			if e.Denied {
				continue
			}
			if editToolNames[e.Name] && !e.IsError {
				if e.CallID == "" || !changed[e.CallID] {
					p := pathArg(e.Arguments)
					edits = append(edits, edit{path: p, idx: i, turn: e.Turn, exempt: p != "" && exempt(p)})
				}
				continue
			}
			if e.Name != "bash" || e.CallID == "" {
				continue
			}
			if c, ok := bashCheck(e, i, o); ok {
				runs = append(runs, run{Check: c, epoch: len(edits)})
			}
		}
	}
	// An advisory belongs to the failed gate check it was asked about:
	// the latest non-passing gate run in its turn before it.
	for _, ad := range advices {
		for j := len(runs) - 1; j >= 0; j-- {
			r := &runs[j]
			if r.idx < ad.idx && r.Turn == ad.turn && (r.Source == session.GateVerify || r.Source == session.GateAccept) && r.Status != Passed {
				a := ad.a
				r.Advisory = &a
				break
			}
		}
	}

	// Edits in scope: every edit up to the end of the turn read.
	var scope []edit
	for _, ed := range edits {
		if o.Turn == 0 || ed.turn <= o.Turn {
			scope = append(scope, ed)
		}
	}
	needing := map[string]bool{}
	for _, ed := range scope {
		if ed.exempt || (o.Turn != 0 && ed.turn != o.Turn) {
			continue
		}
		needing[ed.path] = true
	}

	// One entry per command: its latest run, with the runs on the same
	// unchanged tree counted as attempts.
	last := map[string]int{}
	var order []string
	for j, r := range runs {
		if _, ok := last[r.Command]; !ok {
			order = append(order, r.Command)
		}
		last[r.Command] = j
	}
	sort.SliceStable(order, func(a, b int) bool { return runs[last[order[a]]].idx < runs[last[order[b]]].idx })
	rep := Report{Required: len(needing) > 0, Edited: len(needing)}
	for _, cmd := range order {
		rr := runs[last[cmd]]
		if o.Turn != 0 && rr.Turn != o.Turn {
			continue
		}
		c := rr.Check
		passed, failed := false, false
		for _, x := range runs {
			if x.Command != cmd || x.epoch != rr.epoch {
				continue
			}
			c.Attempts++
			switch x.Status {
			case Passed:
				passed = true
			case Failed:
				failed = true
			}
		}
		if passed && failed {
			c.Status = Flaky
			c.Summary = "passed and failed on an unchanged tree: " + c.Summary
		}
		c.Fresh = true
		files := map[string]bool{}
		for _, ed := range scope {
			if ed.exempt {
				continue
			}
			if ed.idx < c.idx {
				if o.Turn == 0 || ed.turn == o.Turn {
					files[rel(ed.path)] = true
				}
			} else if c.Fresh {
				c.Fresh = false
				c.StaleBy = rel(ed.path)
			}
		}
		for f := range files {
			c.Files = append(c.Files, f)
		}
		sort.Strings(c.Files)
		c.Files = slices.Clip(c.Files[:min(len(c.Files), maxFiles)])
		rep.Checks = append(rep.Checks, c)
	}
	rep.assess()
	return rep
}

// gateCheck reads one TypeGate event.
func gateCheck(e session.Event, idx int, root string) Check {
	var ex GateExtra
	hasExtra := len(e.Extra) > 0 && json.Unmarshal(e.Extra, &ex) == nil
	c := Check{
		Command: shell.TrimWorkspaceCd(strings.TrimSpace(e.Name), root),
		Source:  e.Source, Turn: e.Turn, Exit: ex.Exit, Summary: ex.Summary,
		Ended: e.Time, idx: idx,
	}
	if c.Source == "" {
		c.Source = session.GateVerify
	}
	if e.DurationMs > 0 && !e.Time.IsZero() {
		c.Started = e.Time.Add(-time.Duration(e.DurationMs) * time.Millisecond)
	}
	if c.Source == session.GateAccept {
		// The accept gate's reported name is a label, not the script
		// that ran (see verify.Runner.Display).
		c.Withheld = true
	}
	switch {
	case hasExtra && ex.Status != "":
		c.Status = Parse(ex.Status)
	case e.IsError:
		if !hasExtra {
			c.Exit = ExitUnknown
		}
		c.Status = Classify(c.Exit, e.Output, false)
	default:
		c.Status = Classify(0, e.Output, false)
	}
	if c.Summary == "" && e.Output != "" {
		c.Summary = Summarize(e.Output)
	}
	return c
}

// bashCheck reads a bash tool result that is one of the model's own
// checks. A call the harness refused, or a command that never started
// (not found), is no check; one the user cancelled is a cancelled check.
func bashCheck(e session.Event, idx int, o Options) (Check, bool) {
	cmd := shell.TrimWorkspaceCd(shell.CommandOf(e.Arguments), o.Root)
	if !shell.IsCheck(cmd) {
		return Check{}, false
	}
	c := Check{Command: cmd, Source: "bash", Turn: e.Turn, Ended: e.Time, idx: idx}
	if e.DurationMs > 0 && !e.Time.IsZero() {
		c.Started = e.Time.Add(-time.Duration(e.DurationMs) * time.Millisecond)
	}
	if e.IsError {
		for _, p := range o.Interrupted {
			if strings.HasPrefix(e.Output, p) {
				c.Status, c.Exit, c.Summary = Cancelled, ExitUnknown, "cancelled before it finished"
				return c, true
			}
		}
		for _, p := range o.Refusals {
			if strings.HasPrefix(e.Output, p) {
				return Check{}, false
			}
		}
	}
	if strings.HasPrefix(e.Output, "Started background job ") || strings.HasPrefix(e.Output, "Still running after ") {
		c.Status, c.Exit, c.Summary = Inconclusive, 0, "still running in the background; no result yet"
		return c, true
	}
	c.Exit = bashExit(e)
	if c.Exit == 127 && notFoundRe.MatchString(e.Output) {
		return Check{}, false
	}
	c.Status = Classify(c.Exit, e.Output, false)
	c.Summary = Summarize(e.Output)
	return c, true
}

// bashExit is the exit code in the bash tool's "[exit code N]" line.
func bashExit(e session.Event) int {
	if m := exitLineRe.FindAllStringSubmatch(e.Output, -1); len(m) > 0 {
		if n, err := strconv.Atoi(m[len(m)-1][1]); err == nil {
			return n
		}
	}
	if e.IsError {
		return ExitUnknown
	}
	return 0
}

func pathArg(arguments string) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(arguments), &in) != nil {
		return ""
	}
	return in.Path
}

// adviceOf reads a decision-service record about a verification failure.
func adviceOf(e session.Event) (Advisory, bool) {
	if e.Name != "verify_triage" || len(e.Extra) == 0 {
		return Advisory{}, false
	}
	var x struct {
		Consumer string  `json:"consumer"`
		Status   string  `json:"status"`
		Error    string  `json:"error"`
		Class    string  `json:"class"`
		Conf     float64 `json:"confidence"`
		Caused   float64 `json:"caused_by_change"`
		Skip     bool    `json:"skip"`
	}
	if json.Unmarshal(e.Extra, &x) != nil {
		return Advisory{}, false
	}
	a := Advisory{Source: "jev", Consumer: x.Consumer, Class: x.Class, Confidence: x.Conf, CausedBy: x.Caused, Acted: x.Skip}
	if x.Status != "" {
		a.Error = x.Status
	}
	return a, true
}
