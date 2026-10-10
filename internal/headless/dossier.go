package headless

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/dossier"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/session"
)

// A failed run leaves a failure dossier (internal/dossier): the smallest
// reproduction, the hypotheses it tried and what ruled each out, the last
// red output and a pointer to the best state it reached. It is written
// beside the session's log as <id>.dossier.md and carried in the result
// object as "dossier". It is folded from the session's own record with no
// model call and nothing rerun, and it lands in janetcode's data
// directory, never in the project, so it is on by default;
// JANETCODE_OFF=dossier turns it off.

// dossierVerdicts are the last-turn verdicts that make a run that ended
// without an error count as failed.
var dossierVerdicts = map[string]bool{
	string(agent.VerdictCheckFailed):     true,
	string(agent.VerdictNotDemonstrated): true,
	string(agent.VerdictVacuous):         true,
}

// failureDossier builds and writes the dossier for a run that failed, and
// returns it for the result object; nil for a run that did not fail, had
// nothing to hand off, or has the dossier switched off.
func failureDossier(opts Options, rec *recorder, err error) *dossier.Dossier {
	ag := opts.Agent
	if ag == nil || ag.Off["dossier"] || ag.Log == nil || ag.DryRunOn() {
		return nil
	}
	rec.mu.Lock()
	id, start := rec.sessionID, rec.start
	rec.mu.Unlock()
	all := ag.Log.Events()
	last := 0
	for _, e := range all {
		last = max(last, e.Turn)
	}
	verdict := string(agent.FoldOutcomeIn(all, last, ag.RootDir()).Verdict)
	outcome := ""
	switch {
	case err != nil && (errors.Is(err, ErrConflict) || errors.Is(err, ErrBadInput)):
		// A confirmed conflict is a finished run, and bad input never
		// started one.
		return nil
	case err != nil:
		outcome = err.Error()
	case dossierVerdicts[verdict]:
		outcome = "the last turn ended " + verdict
	default:
		return nil
	}
	// Only this run's part of a resumed session: what an earlier run
	// tried is that run's story.
	var events []session.Event
	for _, e := range all {
		if !e.Time.Before(start) {
			events = append(events, e)
		}
	}
	var changes []change.Change
	for _, c := range ag.Changes() {
		if !c.When.Before(start) {
			changes = append(changes, c)
		}
	}
	d := dossier.Build(id, opts.Prompt, outcome, ag.RootDir(), events, changes)
	if d.Empty() {
		return nil
	}
	if p := strings.TrimSuffix(ag.UndoPath, ".undo.json"); p != "" && p != ag.UndoPath {
		path := p + ".dossier.md"
		// A session sidecar holds what the session saw, so it is private
		// like the log beside it.
		if werr := fsutil.AtomicWriteMode(path, []byte(d.Markdown()), fsutil.PrivateFileMode); werr != nil {
			fmt.Fprintf(opts.Stderr, "[dossier] %v\n", werr)
		} else {
			d.Path = path
			fmt.Fprintf(opts.Stderr, "[dossier] %s\n", path)
		}
	}
	return &d
}
