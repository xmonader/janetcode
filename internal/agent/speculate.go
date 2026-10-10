package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// Speculation during the user's think time. Once a turn has ended and the
// follow-up suggestion has named the prompt the user will probably send
// next, the harness runs that prompt ahead of them — on the cheap model,
// in a throwaway checkout of the project, under a cost cap of its own —
// while they are still reading the reply. If the prompt they send is that
// one, the checkout's changes are written into the project before the
// turn's first model call, and the main model is told they are there and
// reviews them instead of writing them. Anything else they send, and
// anything that moved the tree or the conversation meanwhile, throws the
// run away. Nothing the speculative run does reaches the real tree until
// it is adopted.
//
// It is opt-in (config speculate): it spends money on a prompt the user
// may never send, and adoption writes to the project on the user's
// behalf.

// specMaxUSD is what one speculative run may spend. A run that is not
// adopted is pure cost, so the cap is a fixed small amount rather than a
// share of whatever the session has left.
const specMaxUSD = 0.05

// specMaxIter bounds a speculative run's model calls: the follow-up is a
// short next step, and a run that needs more than this was not one.
const specMaxIter = 12

// specBrief is what the speculative run is told on top of the session's
// system prompt.
const specBrief = "\n\nYou are running the user's next prompt ahead of them, in your own checkout of the project, before they have sent it. Do the work as if they had: make the change, check it with what you are allowed to run, and finish with a short report of what you changed. Do not commit, push, install anything or touch anything outside the checkout. Do not ask questions; make reasonable assumptions and state them."

// specState is the one speculative run a session may have.
type specState struct {
	mu  sync.Mutex
	run *specRun
}

// specRun is a speculative run, started or finished.
type specRun struct {
	prompt string
	model  string
	// msgs is how long the conversation was when the run started: a
	// run is adopted only into the conversation it was started from.
	msgs   int
	digest string
	wt     *worktree
	child  *Agent
	cancel context.CancelFunc
	done   chan struct{}
	err    error
	billed bool
}

// SetSpeculate turns speculation on or off. Off discards a run in
// flight.
func (a *Agent) SetSpeculate(on bool) {
	a.featMu.Lock()
	a.Speculate = on
	a.featMu.Unlock()
	if !on {
		a.DiscardSpeculation()
	}
}

// SpeculateOn is Speculate, safe to read while a turn runs.
func (a *Agent) SpeculateOn() bool {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return a.Speculate
}

// specOutward matches prompts whose next step reaches past the working
// tree — a commit, a push, a deploy, a package install — or undoes
// something: speculating those would either do it for real or rehearse
// something that cannot be adopted as file changes.
var specOutward = regexp.MustCompile(`(?i)\b(commit|push|pull|deploy|publish|release|merge|rebase|reset|revert|undo|delete|remove|rm|drop|install|uninstall|upgrade|send|email|post|tag)\b`)

// specAlternative matches a prompt that hedges between options, which is
// not a single clear next step.
var specAlternative = regexp.MustCompile(`(?i)\b(or|maybe|either|perhaps)\b`)

// specUnclear says why prompt is not a single clear next step worth
// running ahead, or "" when it is.
func specUnclear(prompt string) string {
	p := strings.TrimSpace(prompt)
	switch {
	case len(strings.Fields(p)) < 2:
		return "the suggestion is too short to act on"
	case strings.HasSuffix(p, "?"):
		return "the suggestion is a question"
	case specAlternative.MatchString(p):
		return "the suggestion hedges between options"
	case specOutward.MatchString(p):
		return "the suggestion reaches outside the working tree"
	}
	return ""
}

// specNormal is a prompt reduced to what decides whether the user sent
// the predicted one: case, runs of space and trailing punctuation do not.
func specNormal(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, ".!; ")
}

// StartSpeculation starts a speculative run of prompt, the follow-up the user is
// predicted to send next, when everything allows it: speculation is on,
// no turn is running, prompt is a single clear step, the cheap slot is set
// and priced, the session's own ceiling has room for the cap, and the
// project is a git repository. It reports whether a run started and, when
// not, why. life bounds the run: the front-end's lifetime.
func (a *Agent) StartSpeculation(life context.Context, prompt string) (bool, string) {
	if !a.SpeculateOn() {
		return false, "speculation is off"
	}
	if a.running.Load() {
		return false, "a turn is running"
	}
	if why := specUnclear(prompt); why != "" {
		return false, why
	}
	ps := a.Settings()
	if ps.readOnly() {
		return false, "the session is read-only"
	}
	cheap := a.RouteCheapName()
	if cheap == "" {
		return false, "no cheap model is set (route_cheap)"
	}
	prov, model := ps.Provider, cheap
	if a.Route != nil {
		p, m, err := a.Route(cheap)
		if err != nil {
			return false, "the cheap model cannot be routed: " + err.Error()
		}
		if p != nil {
			prov = p
		}
		model = m
	}
	if prov == nil {
		return false, "no provider"
	}
	if a.PriceOf(model) == nil {
		// An unpriced model makes the cap meaningless.
		return false, "the cheap model has no price, so its cost cannot be capped"
	}
	if ceiling := a.MaxCost(); ceiling > 0 {
		if spent, over := a.overBudget(); over || ceiling-spent < specMaxUSD {
			return false, "the session's cost ceiling has no room"
		}
	}
	root := a.RootDir()
	if root == "" {
		return false, "no project root"
	}
	a.DiscardSpeculation()
	ctx, cancel := context.WithCancel(life)
	wt, err := newWorktree(ctx, root)
	if err != nil {
		cancel()
		return false, err.Error()
	}
	history := closeOpenCalls(copyMessages(a.MessagesSnapshot()))
	child, err := a.specChild(wt, prov, model, history)
	if err != nil {
		wt.remove(context.WithoutCancel(ctx))
		cancel()
		return false, err.Error()
	}
	run := &specRun{prompt: prompt, model: model, msgs: len(history), digest: specDigest(history), wt: wt, child: child, cancel: cancel, done: make(chan struct{})}
	a.spec.mu.Lock()
	a.spec.run = run
	a.spec.mu.Unlock()
	go func() {
		defer close(run.done)
		run.err = child.Run(ctx, prompt, func(Event) {})
	}()
	return true, ""
}

// specChild builds the speculative run: the conversation so far, the
// editing tools rooted at the checkout, the cheap model, and an approver
// that lets the file tools through (they cannot leave the checkout) and
// nothing else the session's own rules do not already allow.
func (a *Agent) specChild(wt *worktree, prov llm.Provider, model string, history []llm.Message) (*Agent, error) {
	ps := a.Settings()
	child, err := New(Deps{
		Provider: prov,
		Tools:    tool.NewRegistry(wt.tools(a.Files)...),
	}, Settings{
		Model:         model,
		MaxTokens:     ps.MaxTokens,
		Effort:        ps.Effort,
		MaxIterations: specMaxIter,
		CompactAt:     ps.CompactAt,
		ContextWindow: a.WindowOf(model),
	})
	if err != nil {
		return nil, err
	}
	child.System = a.System + specBrief
	child.Messages = history
	child.Rules = a.RulesSnapshot()
	child.Protected = a.Protected
	child.EgressPolicy = a.EgressPolicy
	a.shareTaint(child)
	child.Approver = specApprover{}
	child.Prices = a.PriceOf
	child.SetMaxCost(specMaxUSD)
	withholdFromChild(child.Tools, nil)
	return child, nil
}

// specApprover answers the questions a speculative run would put to the
// user, who has not asked for anything yet: file tools rooted at the
// checkout go ahead, and every other call — a command the session's rules
// do not already allow — is refused.
type specApprover struct{}

func (specApprover) Approve(ctx context.Context, call ToolCall) (Decision, error) {
	d, _, err := specApprover{}.ApproveWithReason(ctx, call)
	return d, err
}

func (specApprover) ApproveWithReason(_ context.Context, call ToolCall) (Decision, string, error) {
	switch call.Name {
	case "write_file", "edit_file", "notebook_edit":
		return Allow, "", nil
	}
	return Deny, "this is a speculative run ahead of the user; only commands the session already allows can run", nil
}

// DiscardSpeculation stops and removes the speculative run, if any. It is
// what every prompt but the predicted one, and the end of the session,
// come to.
func (a *Agent) DiscardSpeculation() {
	a.spec.mu.Lock()
	run := a.spec.run
	a.spec.run = nil
	a.spec.mu.Unlock()
	if run != nil {
		a.specDrop(run)
	}
}

// specDrop cancels run, waits for it and removes its checkout. What it
// spent is billed: a discarded run still cost money.
func (a *Agent) specDrop(run *specRun) {
	run.cancel()
	<-run.done
	a.specBill(run)
	run.wt.remove(context.Background())
}

func (a *Agent) specBill(run *specRun) {
	if run.billed {
		return
	}
	run.billed = true
	a.addSideUsage(run.model, run.child.Usage)
}

// specResolve settles the speculative run at the start of a turn whose
// prompt is blocks: adopted when the prompt is the predicted one and
// nothing moved, discarded otherwise. It runs on the turn goroutine after
// the turn has opened, so the changes land under its checkpoint and the
// note goes with its first model call.
func (a *Agent) specResolve(ctx context.Context, blocks []llm.Block, emit func(Event)) {
	a.spec.mu.Lock()
	run := a.spec.run
	a.spec.run = nil
	a.spec.mu.Unlock()
	if run == nil {
		return
	}
	if why := a.specMismatch(run, blocks); why != "" {
		a.specDrop(run)
		emit(Event{Kind: EvNotice, Text: "speculative run discarded: " + why})
		return
	}
	select {
	case <-run.done:
	default:
		emit(Event{Kind: EvNotice, Text: "waiting for the speculative run of this prompt to finish"})
		select {
		case <-run.done:
		case <-ctx.Done():
			a.specDrop(run)
			return
		}
	}
	a.specBill(run)
	defer run.wt.remove(context.WithoutCancel(ctx))
	note, why := a.specAdopt(ctx, run)
	if why != "" {
		emit(Event{Kind: EvNotice, Text: "speculative run discarded: " + why})
		return
	}
	a.queueHarness(note)
	emit(Event{Kind: EvNotice, Text: "adopted the speculative run of this prompt (" + run.model + ")"})
}

// specMismatch says why run cannot stand for this turn, or "".
func (a *Agent) specMismatch(run *specRun, blocks []llm.Block) string {
	for _, b := range blocks {
		if b.Type != llm.BlockText {
			return "the prompt carries more than text"
		}
	}
	if specNormal(specTyped(blocks)) != specNormal(run.prompt) {
		return "the prompt sent is not the one predicted"
	}
	// The turn has added one message, opening with the prompt, to the
	// conversation the run started from; anything else (a note, a /clear,
	// a resume) means the run worked from a conversation that is no
	// longer this one. What the harness attached after the prompt (the
	// repository map, memory, hook context) does not count.
	msgs := a.MessagesSnapshot()
	if len(msgs) != run.msgs+1 || specNormal(specTyped(msgs[run.msgs].Blocks)) != specNormal(run.prompt) ||
		specDigest(closeOpenCalls(msgs[:run.msgs])) != run.digest {
		return "the conversation changed since it started"
	}
	return ""
}

// specTyped is what the user typed: the first text block, without a
// repository map glued onto its end.
func specTyped(blocks []llm.Block) string {
	for _, b := range blocks {
		if b.Type == llm.BlockText && !b.Synthetic {
			t := b.Text
			if i := strings.Index(t, "[repository map:"); i >= 0 {
				t = t[:i]
			}
			return t
		}
	}
	return ""
}

// specDigest fingerprints a conversation: who said what, in order.
func specDigest(msgs []llm.Message) string {
	h := sha256.New()
	for _, m := range msgs {
		fmt.Fprintf(h, "%s\x00%d\x00", m.Role, len(m.Blocks))
		for _, b := range m.Blocks {
			fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", b.Type, b.ID, b.Text, b.Input)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// specAdopt writes run's changes into the project and returns the note
// that tells the model, or why it would not. Every changed file must still
// be in the project as the checkout started from it, and the session must
// be allowed to write it without asking.
func (a *Agent) specAdopt(ctx context.Context, run *specRun) (note, why string) {
	if run.err != nil {
		return "", "it failed: " + run.err.Error()
	}
	report := lastAssistantText(run.child.Messages)
	if strings.TrimSpace(report) == "" {
		return "", "it finished without a report"
	}
	w := run.wt
	rels, err := w.candidates(ctx)
	if err != nil {
		return "", err.Error()
	}
	var paths []string
	for _, rel := range rels {
		base := w.baseOf(ctx, rel)
		after := readVersion(w.abs(w.Dir, rel))
		if after.Same(base) {
			continue
		}
		if !readVersion(w.abs(w.Root, rel)).Same(base) {
			return "", rel + " changed in the project while it ran"
		}
		paths = append(paths, rel)
	}
	if refused := (&Task{Parent: a}).mergeRefused(w.Root, paths); refused != "" {
		return "", refused
	}
	if len(paths) > 0 {
		owner := a
		if a.UndoOwner != nil {
			owner = a.UndoOwner
		}
		abs := make([]string, len(paths))
		for i, rel := range paths {
			abs[i] = w.abs(w.Root, rel)
		}
		owner.cp.snapshot(ctx, owner.snaps(), owner.blobDir, mergeTouch{paths: abs}, nil)
		for i, rel := range paths {
			before := readVersion(abs[i])
			after := readVersion(w.abs(w.Dir, rel))
			if err := writeVersion(abs[i], after, w.abs(w.Dir, rel)); err != nil {
				return "", fmt.Sprintf("%s: %v", rel, err)
			}
			owner.record(Change{Turn: a.turnN, Tool: "speculate", Intent: intentLine("speculative run adopted: "+run.prompt, 120),
				Path: abs[i], Existed: before.Exists, Before: before.Data, After: after.Data, Gone: !after.Exists})
			a.logEvent(session.Event{Type: session.TypeFileChange, Name: abs[i], Arguments: contentDigest(before.Data, before.Exists) + "->" + contentDigest(after.Data, after.Exists)})
		}
		a.mutated = true
		a.mutations++
	}
	return specNote(run.model, paths, report), ""
}

// specNote is what the model is told about an adopted run.
func specNote(model string, paths []string, report string) string {
	var b strings.Builder
	b.WriteString("[speculative run adopted] While the user read your last reply, the harness ran this exact prompt ahead of them on a cheaper model (" + model + ") in a separate checkout.")
	if len(paths) == 0 {
		b.WriteString(" It changed no files.")
	} else {
		sorted := append([]string(nil), paths...)
		sort.Strings(sorted)
		b.WriteString(" Its changes are already in the project: " + strings.Join(sorted, ", ") + ".")
	}
	b.WriteString(" Its report:\n\n" + textfmt.TruncateTo(report, 4000) + "\n\nReview that work against the request rather than redoing it: read the changed files, run the relevant checks, and fix or finish whatever it got wrong or left out. Treat its report as a claim to check, not as evidence.")
	return b.String()
}

// LogFollowup records the follow-up the harness predicted, so the prompt
// the user sends next can later be compared with it (cmd/specscreen reads
// these back).
func (a *Agent) LogFollowup(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	a.logEvent(session.Event{Type: session.TypeFollowup, Output: text})
}
