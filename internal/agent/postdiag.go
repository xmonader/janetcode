package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Post-edit language-server checks run beside the turn instead of inside
// the edit. A language server takes from a fraction of a second to its
// whole timeout (eight seconds by default) to answer for a changed file,
// and the edit call used to wait for it every time. Now the edit returns
// at once; the check runs in the background and its findings join the
// tool results of the first step that finds it finished — usually the
// next one, since a model call takes longer than the check. When the model
// says it is done, the checks still running are waited for, and errors
// they found send it back, at most twice a turn (diagRounds), so a turn
// never ends on an error it was not shown. JANETCODE_OFF=asyncdiag puts
// the check back inside the edit.

// diagWait bounds the wait at the end of a turn: the server's own default
// timeout and some slack for a check that started just before.
var diagWait = 10 * time.Second

// diagRounds is how many times a turn's end is sent back for errors.
const diagRounds = 2

type diagJob struct {
	done chan struct{}
	note string
}

type postDiag struct {
	mu   sync.Mutex
	jobs map[string]*diagJob // the latest check per path
	// dirty names paths whose last reported check found problems, so a
	// clean result for them is worth a line and a clean result for any
	// other path is not.
	dirty map[string]bool
}

// startDiagnose checks path in the background. A newer edit to the same
// path supersedes the check of an older one.
func (a *Agent) startDiagnose(ctx context.Context, path string) {
	j := &diagJob{done: make(chan struct{})}
	d := &a.postDiag
	d.mu.Lock()
	if d.jobs == nil {
		d.jobs = map[string]*diagJob{}
	}
	d.jobs[path] = j
	d.mu.Unlock()
	diagnose := a.Diagnose
	go func() {
		defer close(j.done)
		note := diagnose(ctx, path)
		if ctx.Err() != nil {
			note = ""
		}
		j.note = note
	}()
}

// pendingDiagnostics reports whether any check is still out.
func (a *Agent) pendingDiagnostics() bool {
	d := &a.postDiag
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.jobs) > 0
}

// collectDiagnostics takes the finished checks, waiting up to wait for
// the running ones, and returns what they found, one note per path, and
// whether any of it is an error. A check still running after the wait is
// left for a later step.
func (a *Agent) collectDiagnostics(wait time.Duration) (string, bool) {
	d := &a.postDiag
	d.mu.Lock()
	jobs := make(map[string]*diagJob, len(d.jobs))
	for p, j := range d.jobs {
		jobs[p] = j
	}
	d.mu.Unlock()
	if len(jobs) == 0 {
		return "", false
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	paths := make([]string, 0, len(jobs))
	for p := range jobs {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var (
		notes  []string
		errors bool
	)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dirty == nil {
		d.dirty = map[string]bool{}
	}
	for _, p := range paths {
		j := jobs[p]
		if wait > 0 {
			d.mu.Unlock()
			select {
			case <-j.done:
			case <-deadline.C:
				wait = 0
			}
			d.mu.Lock()
		}
		select {
		case <-j.done:
		default:
			continue
		}
		if d.jobs[p] == j {
			delete(d.jobs, p)
		}
		note := strings.TrimSpace(j.note)
		clean := note == "" || strings.HasPrefix(note, "[diagnostics: no problems")
		switch {
		case clean && d.dirty[p]:
			delete(d.dirty, p)
			notes = append(notes, fmt.Sprintf("[diagnostics for %s: no problems now]", p))
		case clean:
		default:
			d.dirty[p] = true
			if strings.Contains(note, ": error: ") {
				errors = true
			}
			notes = append(notes, fmt.Sprintf("[diagnostics for %s, from the edit's background check]\n%s", p, strings.TrimPrefix(note, "[diagnostics]\n")))
		}
	}
	return strings.Join(notes, "\n"), errors
}

// resetDiagnostics drops whatever a previous turn left running: its
// findings describe edits that turn already answered for.
func (a *Agent) resetDiagnostics() {
	d := &a.postDiag
	d.mu.Lock()
	d.jobs = nil
	d.mu.Unlock()
}

// diagnosticsGate waits for the checks a finishing turn still has out and
// sends the model back when they found errors, at most diagRounds times a
// turn. Warnings alone end the turn as before; findings that did not stop
// it are dropped with the turn.
func (a *Agent) diagnosticsGate(rounds *int, emit func(Event)) bool {
	if !a.pendingDiagnostics() {
		return false
	}
	note, errors := a.collectDiagnostics(diagWait)
	if !errors || *rounds >= diagRounds {
		return false
	}
	*rounds++
	emit(Event{Kind: EvNotice, Text: "diagnostics found errors after the last edit: " + firstLine(note)})
	a.appendHarnessText(note + "\n\n[harness] The language server reports errors in files this turn edited. Fix them, or say why they are expected.")
	return true
}

// firstLine is a note's first diagnostic, for a one-line notice: the first
// line that is not one of the note's own headers.
func firstLine(note string) string {
	lines := strings.Split(note, "\n")
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "[") {
			return l
		}
	}
	return strings.TrimSpace(lines[0])
}
