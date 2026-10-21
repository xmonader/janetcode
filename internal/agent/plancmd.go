package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/plan"
)

// This file is /plan, whole: the mode, the file the plan is written to,
// the comments the user leaves on it and the message those become. Both
// front-ends call PlanCommand and render the cmdresult.Result, so the
// terminal and the web UI cannot drift into two different plan modes.
//
// The plan is a file, not a field. A turn that ends with a plan writes
// it to .janetcode/plan.md (capturePlan), and review, changes and
// approve read it back from there. That is the whole reason the review
// loop can exist: a plan you can only scroll back to is not a plan you
// can annotate.

// PlanRevisePrompt is the instruction that rides with the collected
// comments when the session is idle, so the model revises the plan in
// the turn the comments start rather than waiting for the user to think
// of something else to say.
const PlanRevisePrompt = "Revise the plan to address every comment above and write the new plan in full under a `## Plan` heading. Do not start making changes."

// planRoot is the directory the plan file lives under: the session's
// root, or the process working directory when a front-end set none.
func (a *Agent) planRoot() string {
	r := a.RootDir()
	if r == "" {
		return "."
	}
	return r
}

// PlanComments is the collection the user has built up and not yet sent.
func (a *Agent) PlanComments() []plan.Comment { return a.planComments.List() }

// capturePlan writes the plan a plan-mode turn just produced to the plan
// file. A reply with no `## Plan` heading is a question or a note, not a
// plan, and leaves the file alone; a reply that restates the same plan
// rewrites nothing. Failing to write is said out loud rather than
// swallowed: a /plan review that then shows a stale plan would be worse
// than no file at all.
func (a *Agent) capturePlan(text string, emit func(Event)) {
	if !a.Settings().PlanMode {
		return
	}
	body, ok := plan.Extract(text)
	if !ok {
		return
	}
	changed, err := plan.Write(a.planRoot(), body)
	if err != nil {
		emit(Event{Kind: EvNotice, Text: fmt.Sprintf("could not write %s: %v", plan.Name, err)})
		return
	}
	if !changed {
		return
	}
	// The same file the live plan watches (see planfile.go): this write
	// is the model's own plan, not the user steering.
	a.planfileSaw()
	note := "plan written to " + plan.Name + " · /plan review reads it and comments on a line"
	if _, err := os.Stat(plan.PrevFile(a.planRoot())); err == nil {
		note = "plan revised in " + plan.Name + " (the version it replaced is in " + plan.PrevName + ")"
	}
	emit(Event{Kind: EvNotice, Text: note})
}

// PlanCommand is /plan for every front-end: the mode, the review of the
// plan file, the comments on it, and the two ways out of the mode —
// changes, which sends the comments back and stays in plan mode, and
// approve, which leaves it and executes.
//
// A Result's Handover is the turn the front-end should start; running
// reports whether a turn is already in flight, which is the one thing
// only the front-end knows.
func (a *Agent) PlanCommand(arg string, running bool) (cmdresult.Result, error) {
	arg = strings.TrimSpace(arg)
	verb, rest, _ := strings.Cut(arg, " ")
	// Every verb but comment takes no argument, and each is an ordinary
	// English word: with text after it, it is the first word of a
	// request ("/plan go through the handlers…", "/plan exit codes are
	// wrong…"), which used to be approved, reviewed or switched off
	// instead of planned.
	if strings.TrimSpace(rest) != "" {
		if v := strings.ToLower(verb); v != "comment" && v != "c" {
			verb = ""
		}
	}
	switch strings.ToLower(verb) {
	case "review", "show", "read":
		return a.planReview()
	case "comment", "c":
		return a.planComment(rest)
	case "changes", "revise", "comments":
		return a.planChanges(running)
	case "approve", "go", "execute":
		return a.planApprove()
	case "off", "cancel", "exit":
		a.SetPlanMode(false)
		a.planComments.Take()
		return cmdresult.Result{Notes: []string{"plan mode off"}}, nil
	}
	if !a.Settings().PlanMode {
		a.SetPlanMode(true)
	}
	res := cmdresult.Result{Notes: []string{"plan mode on · the model explores and writes a plan to " + plan.Name + " · /plan review reads it, /plan approve runs it"}}
	if arg != "" {
		res.Handover = arg
	}
	return res, nil
}

// planReview shows the plan file with line numbers, the way /open shows
// any file, plus whatever comments are already attached to a line.
func (a *Agent) planReview() (cmdresult.Result, error) {
	body, err := plan.Read(a.planRoot())
	if err != nil {
		return cmdresult.Result{Error: "no plan yet: " + plan.Name + " is written when a plan-mode turn ends with a plan"}, nil
	}
	comments := a.planComments.List()
	res := cmdresult.Result{
		SheetTitle: plan.Name,
		Sheet:      fmt.Sprintf("# %s  %d lines\n", plan.Name, len(plan.Lines(body))) + plan.Numbered(body, comments),
	}
	res.Tips = append(res.Tips, "/plan comment <line> <what you want changed> attaches a comment to that line")
	if n := len(comments); n > 0 {
		res.Tips = append(res.Tips, fmt.Sprintf("%d comment(s) collected · /plan changes sends them all at once · /plan approve accepts the plan as written", n))
	} else {
		res.Tips = append(res.Tips, "/plan approve accepts the plan as written")
	}
	return res, nil
}

// planComment attaches one comment to one line of the plan.
func (a *Agent) planComment(arg string) (cmdresult.Result, error) {
	body, err := plan.Read(a.planRoot())
	if err != nil {
		return cmdresult.Result{Error: "no plan to comment on: " + plan.Name + " does not exist yet"}, nil
	}
	line, text, ok := plan.ParseRef(arg)
	if !ok {
		return cmdresult.Result{Error: "usage: /plan comment <line> <comment>  (the line numbers are the ones /plan review shows)"}, nil
	}
	if n := len(plan.Lines(body)); line > n {
		return cmdresult.Result{Error: fmt.Sprintf("%s has %d lines", plan.Name, n)}, nil
	}
	if text == "" {
		return cmdresult.Result{Error: "a comment needs something to say: /plan comment " + arg + " <what you want changed>"}, nil
	}
	a.planComments.Add(line, text)
	return cmdresult.Result{Notes: []string{fmt.Sprintf("%s:%d commented · %d waiting · /plan changes sends them",
		plan.Name, line, a.planComments.Len())}}, nil
}

// planChanges sends every collected comment to the model as one message
// and stays in plan mode: asking for changes is not approving.
func (a *Agent) planChanges(running bool) (cmdresult.Result, error) {
	if a.planComments.Len() == 0 {
		return cmdresult.Result{Error: "no comments collected: /plan review, then /plan comment <line> <what you want changed>"}, nil
	}
	body, err := plan.Read(a.planRoot())
	if err != nil {
		return cmdresult.Result{Error: "no plan to change: " + plan.Name + " does not exist yet"}, nil
	}
	comments := a.planComments.Take()
	msg := plan.Message(body, comments)
	if !a.Settings().PlanMode {
		a.SetPlanMode(true)
	}
	note := fmt.Sprintf("%d comment(s) sent as one message · still in plan mode", len(comments))
	if running {
		// The turn already running is the one writing the plan; steering
		// reaches it before its next model call.
		a.Steer(msg)
		return cmdresult.Result{Notes: []string{note + " · steering the running turn"}}, nil
	}
	// AddContext puts the comments in as the user's own words; the
	// handover merges into that same message, so the model gets one.
	a.AddContext(msg)
	return cmdresult.Result{Notes: []string{note}, Handover: PlanRevisePrompt}, nil
}

// planApprove leaves plan mode and executes the plan on the file.
func (a *Agent) planApprove() (cmdresult.Result, error) {
	if !a.Settings().PlanMode {
		return cmdresult.Result{Error: "not in plan mode: /plan <request> writes a plan first"}, nil
	}
	note := "plan approved"
	if _, err := plan.Read(a.planRoot()); err == nil {
		note = plan.Name + " approved"
	}
	if n := len(a.planComments.Take()); n > 0 {
		note += fmt.Sprintf(" · %d unsent comment(s) dropped", n)
	}
	a.SetPlanMode(false)
	return cmdresult.Result{
		Notes:    []string{note},
		Status:   "plan approved · executing · /undo reverts a step",
		Handover: PlanApprovedPrompt,
	}, nil
}
