package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/hints"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/project"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The terminal's side of the hint engine: it watches what it is already
// watching, and asks the engine whether any of it is worth a line.
//
// Everything here runs on events — an agent event, a turn ending, a
// denial, a paste — and never on a render or a keystroke. Recording an
// observation is a field assignment; evaluating is a walk over the rule
// table's predicates. The only I/O is the one small write that happens
// when a hint fires, which is at most once per rule in the life of a
// project. (The comment used to name a count; the table has grown twice
// since and the number was wrong both times.)

// hintsState is the terminal's whole side of the hint engine: the
// engine itself, the facts the rules are allowed to look at, what the
// observers remember between events, and the !commands seen so far.
type hintsState struct {
	engine   *hints.Engine    // event-driven one-time hints; nil disables them
	state    hints.State      // what the hint rules are allowed to look at
	obs      hintObservations // what the observers remember between events
	bangSeen map[string]int   // !commands the user ran, by command line
}

// off reports whether hints are disabled, either because none were
// configured or because /hints off silenced them. Every observer asks
// first: recording into a state nobody evaluates is pure cost.
func (h *hintsState) off() bool { return h.engine == nil || h.engine.Off }

// maxBangSeen bounds the !command counter. Only lines that look like a
// test or build command get in (see testCommandRe), but there is no
// limit on how many distinct ones a session can type — "go test ./a",
// "go test ./b", one per package — and nothing ever trimmed the map, so
// it grew for the life of the window. The rule it feeds only ever asks
// whether one line has been seen twice, so forgetting the oldest lines
// costs at most a hint that would have fired.
const maxBangSeen = 256

// bang counts one !command the user typed and reports how many times it
// has now been seen.
func (h *hintsState) bang(cmdline string) int {
	if h.bangSeen == nil {
		h.bangSeen = map[string]int{}
	}
	if len(h.bangSeen) >= maxBangSeen {
		if _, seen := h.bangSeen[cmdline]; !seen {
			clear(h.bangSeen)
		}
	}
	h.bangSeen[cmdline]++
	return h.bangSeen[cmdline]
}

// noteHint records an observation and evaluates. A hint that comes out
// goes into the transcript as a tip, the same faint one-line form the
// first-run notes have always used.
func (m *model) noteHint(record func(*hints.State)) {
	if m.hint.off() {
		return
	}
	if record != nil {
		record(&m.hint.state)
	}
	m.refreshHintState()
	if text := m.hint.engine.Eval(&m.hint.state); text != "" {
		m.transcript.tip(text)
		// Only once there is a window: at startup the first layout has
		// not happened yet, and the banner is redrawn with everything
		// else the moment it does.
		if m.width > 0 {
			m.refresh(false)
		}
	}
}

// refreshHintState fills in the parts of the state that are cheap to
// read from what the session already holds: the gauge, the spend, and
// whether the user is being asked something.
func (m *model) refreshHintState() {
	s := &m.hint.state
	s.Blocked = m.mode().blocked()
	s.Promotable = m.opts.Running != nil
	if m.opts.Verify != nil {
		s.VerifyConfigured = len(m.opts.Verify.Config.Commands) > 0
	}
	if ag := m.opts.Agent; ag != nil {
		snap := ag.UsageSnapshot()
		usd, ok := snap.Cost(ag.Settings().Price)
		if ok {
			s.CostUSD = usd
		}
		s.Spent = snap.InputTokens > 0 || snap.OutputTokens > 0
		s.CostKnown = ok
		// The gauge is already computed for the status line; this reads
		// the estimate the agent published rather than walking the
		// history again.
		if limit := ag.CompactThreshold(); limit > 0 {
			est := ag.TokenEstimate()
			if !m.turn.running {
				est = ag.EstimateTokens()
			}
			s.ContextPct = est * 100 / limit
		}
	}
	// A model other than the one the last turn ran on is a switch made
	// mid-session, by /model or the picker; either way it is seen here.
	if ag := m.opts.Agent; ag != nil && m.hint.obs.turns > 0 {
		if cur := ag.Settings().Model; cur != "" && cur != m.hint.obs.turnModel {
			s.SwitchedModel = cur
			if m.opts.Session != nil {
				s.SessionID = m.opts.Session.Meta.ID
			}
		}
	}
	s.Used = m.featureUsed
}

// featureUsed reports whether the user has reached for a feature
// before, so that a hint never introduces something they already know.
func (m *model) featureUsed(name string) bool { return m.opts.Usage.Used(name) }

// noteUse records that the user reached for a feature. It is a map
// increment on an event that was being handled anyway; the file is
// written once, when the session ends.
func (m *model) noteUse(name string) {
	if name == "" {
		return
	}
	m.opts.Usage.Note(slash.Canonical(name))
}

// firstRunID is the id the first-run project line is recorded under, in
// the same per-project file the hint rules use. It is not a rule — the
// engine never evaluates it — only a name for "this has been said".
const firstRunID = "first-run-project"

// firstRunLine is what a directory janetcode has never seen before gets
// instead of a generic tip: one line naming what the project looks like,
// the verification command ready to paste, and the language server if it
// is not installed. Nothing is run and nothing is written — proposing a
// command is not the same as running it, and a directory nobody has
// asked janetcode to manage does not get a .janetcode written into it.
func (m *model) firstRunLine() string {
	if m.opts.Cwd == "" || m.opts.Resumed > 0 {
		return ""
	}
	// A .janetcode directory means this project has been set up already,
	// whatever is in it.
	if _, err := os.Stat(filepath.Join(m.opts.Cwd, ".janetcode")); err == nil {
		return ""
	}
	if m.opts.Verify != nil && len(m.opts.Verify.Config.Commands) > 0 {
		return ""
	}
	p, ok := project.Detect(m.opts.Cwd)
	if !ok {
		return ""
	}
	return p.Line(onPath)
}

// onPath reports whether a binary is reachable. It is the one exec-ish
// call on the startup path and it does no more than walk PATH.
func onPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// hintsCommand is /hints on|off.
func (m *model) hintsCommand(args []string) {
	if m.hint.engine == nil {
		m.note("hints are not available in this session")
		return
	}
	switch {
	case len(args) == 0:
		// Whichever way round it is, the line has to name the other
		// way: somebody who has just turned hints off and typed /hints
		// was being told again how to turn them off.
		if m.hint.engine.Off {
			m.note("hints are off · /hints on brings them back")
			return
		}
		// Said and spent are different things: a rule suppressed because
		// the first-run line had already proposed it was never a hint
		// anybody read, and counting it as given overstated the help.
		line := textfmt.Plural(len(m.hint.engine.Shown()), "hint") + " shown for this project"
		if n := len(m.hint.engine.Skipped()); n > 0 {
			line += fmt.Sprintf(", %d skipped", n)
		}
		m.note(line + " · /hints off silences them")
	case args[0] == "on":
		m.hint.engine.Off = false
		m.note("hints on")
	case args[0] == "off":
		m.hint.engine.Off = true
		m.note("hints off · \"hints\": false in the config file makes it permanent")
	default:
		m.transcript.errorf("usage: /hints [on|off]")
	}
}

// slowCommandSeconds is how long a foreground command has to hold a turn
// before ctrl+g is worth mentioning.
const slowCommandSeconds = 20

// observeEvent is called from applyEvent for the kinds the hints care
// about. It is a switch over four event kinds, on a path that already
// switches over ten.
func (m *model) observeEvent(ev agent.Event) {
	if m.hint.off() {
		return
	}
	switch ev.Kind {
	case agent.EvTurnDone:
		m.observeTurnDone()
		return
	case agent.EvVerdict:
		// A fix of something that had been failing, not any green turn: a
		// first build of a new project going green is not a regression
		// worth keeping, and the tip read as jargon there.
		if !ev.IsError && verdictWord(ev.Text) == string(agent.VerdictVerified) && strings.Contains(ev.Text, agent.FlippedWhy) {
			m.noteHint(func(s *hints.State) { s.VerifiedAfterFailure = true })
		}
		return
	case agent.EvNotice:
		if strings.HasPrefix(ev.Text, agent.ProofAttachedNotice) {
			m.noteHint(func(s *hints.State) { s.ProofAttached = true })
		}
		return
	}
	if ev.Kind != agent.EvToolResult || ev.Call == nil || ev.Call.Name != "bash" {
		return
	}
	secs := int(ev.Duration.Seconds())
	if secs < slowCommandSeconds || secs <= m.hint.state.SlowCommandSeconds {
		return
	}
	m.noteHint(func(s *hints.State) {
		s.SlowCommandSeconds = secs
		s.SlowCommand = ev.Call.Name
	})
}

// testCommandRe recognises the commands a user runs by hand over and
// over and should have handed to the verification gate instead.
var testCommandRe = regexp.MustCompile(`(?i)\b(go test|pytest|npm (run )?test|yarn test|cargo (test|build)|make (test|check)|go build|go vet|tsc|jest|mvn|gradle|rspec|phpunit|ctest)\b`)

// observeBang records a shell command the user ran themselves. The
// second time the same test command is typed, the gate is worth naming.
func (m *model) observeBang(cmdline string) {
	if m.hint.off() {
		return
	}
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" || !testCommandRe.MatchString(cmdline) {
		return
	}
	if m.hint.bang(cmdline) < 2 {
		return
	}
	m.noteHint(func(s *hints.State) { s.RepeatedBangCommand = cmdline })
}

// observeDenial reads the refusal count the agent has already recorded
// in the ledger for shape, the rule shape the r key would have saved.
// The ledger, not an in-memory count here, is what makes this survive a
// session that ends and a new one that starts on the same project.
func (m *model) observeDenial(shape string) {
	if m.hint.off() || shape == "" {
		return
	}
	ag := m.opts.Agent
	if ag == nil {
		return
	}
	tally := ag.Ledger.Get(shape)
	if tally.Denied < 3 {
		return
	}
	m.noteHint(func(s *hints.State) {
		s.DeniedShape, s.DeniedCount = shape, tally.Denied
	})
}

// observeApproval reads the ledger's tally for a shape the user just
// approved: once it has been allowed perm.LearnAfter times with no
// refusal, the learned-rule hint offers to save it before the user has
// to keep answering the same prompt.
func (m *model) observeApproval(shape string) {
	if m.hint.off() || shape == "" {
		return
	}
	ag := m.opts.Agent
	if ag == nil {
		return
	}
	tally := ag.Ledger.Get(shape)
	if tally.Allowed < perm.LearnAfter || tally.Denied != 0 {
		return
	}
	m.noteHint(func(s *hints.State) {
		s.ApprovedShape, s.ApprovedCount = shape, tally.Allowed
	})
}

// traceRe matches the shapes of a pasted stack trace or diagnostic dump
// in the languages janetcode is most often pointed at.
var traceRe = regexp.MustCompile(`(?m)^\s*(at [\w.$]+\(|File "|\s+File |goroutine \d+ \[|panic: |Traceback \(most recent|\tat |Caused by: |\s+at .*\.(java|kt|scala|cs):\d+)`)

// observePaste is the one hint that runs from a key event, because a
// bracketed paste *is* one event: the whole text arrives at once, and
// the moment to mention @path is while the user is looking at what they
// just pasted.
func (m *model) observePaste(text string) {
	if m.hint.off() {
		return
	}
	lines := strings.Count(text, "\n") + 1
	trace := traceRe.MatchString(text)
	if lines < 40 && !trace {
		return
	}
	m.noteHint(func(s *hints.State) {
		s.PasteLines, s.PasteLooksLikeTrace = lines, trace
	})
}

// hintObservations is what the observers below remember between events:
// the shapes of this session's changing turns, and the model the last
// turn ran on.
type hintObservations struct {
	shapes    map[string]bool
	turns     int
	turnModel string
}

// verdictWord is the verdict of a verdict line without its mark:
// "✓ verified: …" is "verified".
func verdictWord(line string) string {
	b := verdictBrief(line)
	if i := strings.IndexByte(b, ' '); i >= 0 {
		b = b[i+1:]
	}
	return strings.TrimSpace(b)
}

// observeTurnDone runs once per turn that ended normally. It remembers
// the model the turn ran on, for the what-if hint, and compares the
// turn's file changes with the earlier turns' for the recipe hint.
func (m *model) observeTurnDone() {
	ag := m.opts.Agent
	if ag == nil {
		return
	}
	o := &m.hint.obs
	shape, steps := m.lastTurnShape()
	// One file edited twice is most of programming; a repeated sequence
	// of at least two changes is a procedure.
	repeated := steps >= 2 && o.shapes[shape]
	if steps >= 2 {
		if o.shapes == nil {
			o.shapes = map[string]bool{}
		}
		o.shapes[shape] = true
	}
	// Evaluated before the turn's model is recorded, so a switch made
	// before this turn is still seen as one.
	m.noteHint(func(s *hints.State) {
		if repeated {
			s.RepeatedTurnShape = textfmt.FirstLine(shape, 70)
		}
	})
	o.turns++
	o.turnModel = ag.Settings().Model
}

// lastTurnShape is the normalised signature of the last turn's file
// changes: each tool and file in order, with a run of changes to the
// same file by the same tool counted once. It reads the provenance
// record the agent already keeps rather than tracking calls here.
func (m *model) lastTurnShape() (string, int) {
	ag := m.opts.Agent
	if !ag.LastTurnMutated() {
		return "", 0
	}
	changes := ag.Changes()
	last := 0
	for _, c := range changes {
		last = max(last, c.Turn)
	}
	var steps []string
	for _, c := range changes {
		if c.Turn != last || c.Undone {
			continue
		}
		step := c.Tool + " " + m.relPath(c.Path)
		if n := len(steps); n > 0 && steps[n-1] == step {
			continue
		}
		steps = append(steps, step)
	}
	return strings.Join(steps, ", "), len(steps)
}

// undoSpread counts the distinct files and the distinct intents of the
// turn the next /undo reverts, whichever is more. It has to be read
// before the undo, while the turn is still the one Undo names.
func (m *model) undoSpread() int {
	if m.hint.off() || m.opts.Agent == nil {
		return 0
	}
	turn, ok := m.opts.Agent.UndoTurn()
	if !ok {
		return 0
	}
	files, intents := map[string]bool{}, map[string]bool{}
	for _, c := range m.opts.Agent.Changes() {
		if c.Turn != turn || c.Undone {
			continue
		}
		files[c.Path] = true
		if c.Intent != "" {
			intents[c.Intent] = true
		}
	}
	return max(len(files), len(intents))
}

// observeUndo is called after a whole-turn /undo reverted something,
// with what undoSpread counted beforehand.
func (m *model) observeUndo(spread int) {
	if m.hint.off() || spread < 2 {
		return
	}
	m.noteHint(func(s *hints.State) { s.UndoneMixed = spread })
}

// subcommandUses are the subcommands counted as uses of their own, for
// the hints that teach a subcommand of a command the user already types.
var subcommandUses = map[string]bool{
	"/undo about": true, "/recipe save": true, "/eval keep": true, "/proof push": true,
}

// observeCommand runs after a slash command has been handled. It counts
// the subcommands above, and after /model it asks whether a switch
// mid-session is worth the what-if hint.
func (m *model) observeCommand(fields []string) {
	if len(fields) > 1 {
		if sub := slash.Canonical(fields[0]) + " " + fields[1]; subcommandUses[sub] {
			m.noteUse(sub)
		}
	}
	if len(fields) > 1 && slash.Canonical(fields[0]) == "/model" {
		m.noteHint(nil)
	}
}

// testFirstRe recognises a prompt that asks for tests to be written.
var testFirstRe = regexp.MustCompile(`(?i)\b((add|write|with|plus|including|include)\s+(some\s+|the\s+|new\s+|unit\s+|integration\s+|acceptance\s+|regression\s+)*tests?|test[- ]first|test[- ]driven|tdd)\b`)

// bigFeatureRe recognises the opening of a feature request; with enough
// words after it, it is the kind of request acceptance tests pin down.
var bigFeatureRe = regexp.MustCompile(`(?i)^\s*(please\s+)?(implement|add|build|create)\b`)

// bigFeatureWords is how long a feature request has to be to count.
const bigFeatureWords = 80

// observePrompt looks at a prompt the user typed, as it is sent.
func (m *model) observePrompt(text string) {
	if m.hint.off() {
		return
	}
	if !testFirstRe.MatchString(text) && !(bigFeatureRe.MatchString(text) && len(strings.Fields(text)) >= bigFeatureWords) {
		return
	}
	m.noteHint(func(s *hints.State) { s.TestFirstPrompt = true })
}
