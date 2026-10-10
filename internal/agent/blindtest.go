package agent

import (
	"context"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/blindtest"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/session"
)

// The blind tester (BlindTest): when a headless turn that changed files
// ends with its checks green, one completion on a model slot (the cheap
// one by default) is sent the task and the names of the project's files
// as the task found them — never the code, never the transcript — and
// writes a check per rule the task states (internal/blindtest). The
// harness runs the checks in a throwaway copy of the tree; when some
// fail, the turn goes back once with the rules they were written for and
// nothing of the checks. After that round the failed checks are rerun
// once more, without a model call, so the proof note says whether the
// round fixed them.
//
// It is opt-in (blind_tester in the config): it is a model call the user
// did not ask for, it runs model-written commands, and it can send a
// finished turn back. JANETCODE_OFF=blindtest turns it off for a session
// whose config has it on.

// BlindTester is what the blind tester needs from the composition root:
// the model to ask, and how to make a throwaway copy of the tree as it
// stands (localize/tree.Scratch), which the agent may not import.
type BlindTester struct {
	Model   string
	Scratch func(ctx context.Context, root string) (dir string, cleanup func(), err error)
}

const (
	// blindMaxTokens bounds the tester's reply: eight short checks, after
	// whatever the model reasons first. 6,000 was spent entirely on
	// reasoning by MiMo on the first live run (finish=length, no checks),
	// so the bound is the main loop's own.
	blindMaxTokens = 32768
	// blindBudget bounds the wall time of one run of the checks; a check
	// it cuts off is inconclusive.
	blindBudget = 4 * time.Minute
	// blindMaxFiles bounds the file list the tester is shown.
	blindMaxFiles = 200
)

// blindGateState is one turn's blind tester: whether it ran, the checks
// that failed (kept for the one recheck), and the record for the proof
// note.
type blindGateState struct {
	ran, rechecked bool
	violated       []blindtest.Check
	rec            proof.BlindTest
}

// blindTestGate runs the blind tester at the end of a turn and reports
// whether the turn goes back to the model. It runs once a turn, on a turn
// that changed files and passed its checks; the next time the turn ends,
// it reruns the checks that failed and records what they show.
func (a *Agent) blindTestGate(ctx context.Context, outcome TurnOutcome, g *blindGateState, emit func(Event)) bool {
	if a.BlindTest == nil || a.Off["blindtest"] || a.readOnly() || a.DryRunOn() {
		return false
	}
	if g.ran {
		if g.rec.SentBack && !g.rechecked {
			g.rechecked = true
			a.blindRecheck(ctx, g, emit)
		}
		return false
	}
	if outcome.Verdict == VerdictNoChanges || !outcome.OK() {
		return false
	}
	g.ran = true
	g.rec = proof.BlindTest{Model: a.BlindTest.Model}
	if g.rec.Model == "" {
		g.rec.Model = a.Settings().Model
	}
	emit(Event{Kind: EvNotice, Text: "the turn is done; a tester that sees only the task is writing checks for it"})
	spec, files := a.blindSpec()
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, blindtest.Prompt(spec, files))}
	reply, err := a.sideCall(ctx, blindtest.System, msgs, a.BlindTest.Model, blindMaxTokens)
	if ctx.Err() != nil {
		return false
	}
	if reply.Model != "" {
		g.rec.Model = reply.Model
	}
	checks := blindtest.Parse(reply.Text)
	switch {
	case err != nil:
		g.rec.Skipped = "no reply: " + err.Error()
	case len(checks) == 0:
		g.rec.Skipped = "no checks in the reply"
	}
	if g.rec.Skipped != "" {
		a.logBlindTest(g.rec, emit)
		return false
	}
	g.rec.Checks = len(checks)
	outcomes, skipped := a.blindRun(ctx, checks)
	if skipped != "" {
		g.rec.Skipped = skipped
		a.logBlindTest(g.rec, emit)
		return false
	}
	for i, o := range outcomes {
		switch o {
		case blindtest.Held:
			g.rec.Held++
		case blindtest.Violated:
			g.violated = append(g.violated, checks[i])
			g.rec.Violated = append(g.rec.Violated, checks[i].Rule)
		default:
			g.rec.Inconclusive++
		}
	}
	g.rec.SentBack = len(g.violated) > 0
	a.logBlindTest(g.rec, emit)
	if !g.rec.SentBack {
		return false
	}
	a.appendHarnessText(blindtest.Note(g.rec.Violated))
	return true
}

// blindRecheck reruns the checks that failed, after the round they sent
// the turn back for, and records which still fail. No model call.
func (a *Agent) blindRecheck(ctx context.Context, g *blindGateState, emit func(Event)) {
	outcomes, skipped := a.blindRun(ctx, g.violated)
	if skipped != "" {
		return
	}
	g.rec.Rechecked = true
	g.rec.Still = nil
	for i, o := range outcomes {
		if o == blindtest.Violated {
			g.rec.Still = append(g.rec.Still, g.violated[i].Rule)
		}
	}
	a.logBlindTest(g.rec, emit)
}

// blindRun runs checks in one throwaway copy of the tree as it stands and
// judges each, or says why none could run.
func (a *Agent) blindRun(ctx context.Context, checks []blindtest.Check) ([]blindtest.Outcome, string) {
	if a.BlindTest.Scratch == nil {
		return nil, "no throwaway copy"
	}
	ctx, cancel := context.WithTimeout(ctx, blindBudget)
	defer cancel()
	dir, cleanup, err := a.BlindTest.Scratch(ctx, a.RootDir())
	if err != nil {
		return nil, "no throwaway copy: " + err.Error()
	}
	defer cleanup()
	out := make([]blindtest.Outcome, len(checks))
	for i, c := range checks {
		if ctx.Err() != nil {
			break // the rest stay inconclusive
		}
		r := a.runProbe(ctx, c.Cmd, dir)
		out[i] = blindtest.Judge(r.outcome != probeInconclusive, r.outcome == probeFail, r.output)
	}
	return out, ""
}

// blindSpec is what the tester may see: the request, the prose files at
// the top of the project (BRIEF.md, README.md) that this turn did not
// write or change, and the names of the files that were there before the
// turn. Nothing the implementer wrote reaches it.
func (a *Agent) blindSpec() (string, []string) {
	root := a.RootDir()
	touched, created := map[string]bool{}, map[string]bool{}
	for _, e := range a.turnEdits() {
		touched[e.Path] = true
		if !e.Existed {
			created[e.Path] = true
		}
	}
	var spec strings.Builder
	spec.WriteString(a.integ.userText)
	var files []string
	seen := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (skipDir(d.Name()) || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if seen++; seen > ledgerMaxFiles {
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if created[rel] {
			return nil
		}
		if len(files) < blindMaxFiles {
			files = append(files, rel)
		}
		if !touched[rel] && isDocFile(rel) && strings.Count(rel, "/") <= 1 && spec.Len() < blindtest.MaxSpec {
			if data, err := readSmall(p); err == nil {
				spec.WriteString("\n\n--- " + rel + " ---\n")
				spec.Write(data)
			}
		}
		return nil
	})
	return spec.String(), files
}

// logBlindTest puts the record on the session log, where the proof note
// reads it back (foldBlindTest), and tells the user.
func (a *Agent) logBlindTest(rec proof.BlindTest, emit func(Event)) {
	emit(Event{Kind: EvNotice, Text: rec.Line()})
	raw, _ := json.Marshal(rec)
	a.logEvent(session.Event{Type: session.TypeBlindTest, Turn: a.turnN, Name: rec.Model, Output: rec.String(), IsError: len(rec.Violated) > 0, Extra: raw})
}

// foldBlindTest returns the turn's latest blind tester record, or nil.
func foldBlindTest(events []session.Event, turn int) *proof.BlindTest {
	var b *proof.BlindTest
	for _, e := range events {
		if e.Type != session.TypeBlindTest || e.Turn != turn {
			continue
		}
		var x proof.BlindTest
		if json.Unmarshal(e.Extra, &x) == nil {
			b = &x
		}
	}
	return b
}
