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
	"github.com/xmonader/janetcode/internal/project"
)

// The terminal's side of the hint engine: it watches what it is already
// watching, and asks the engine whether any of it is worth a line.
//
// Everything here runs on events — an agent event, a turn ending, a
// denial, a paste — and never on a render or a keystroke. Recording an
// observation is a field assignment; evaluating is a walk over nine
// predicates. The only I/O is the one small write that happens when a
// hint fires, which happens at most nine times in the life of a project.

// noteHint records an observation and evaluates. A hint that comes out
// goes into the transcript as a tip, the same faint one-line form the
// first-run notes have always used.
func (m *model) noteHint(record func(*hints.State)) {
	if m.hints == nil || m.hints.Off {
		return
	}
	if record != nil {
		record(&m.hintState)
	}
	m.refreshHintState()
	if text := m.hints.Eval(&m.hintState); text != "" {
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
	s := &m.hintState
	s.Blocked = m.pending != nil || m.picker != nil || m.connect != nil || m.denying
	s.Promotable = m.opts.Running != nil
	if m.opts.Verify != nil {
		s.VerifyConfigured = len(m.opts.Verify.Config.Commands) > 0
	}
	if ag := m.opts.Agent; ag != nil {
		snap := ag.UsageSnapshot()
		usd, ok := snap.Cost(ag.Price)
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
			if !m.running {
				est = ag.EstimateTokens()
			}
			s.ContextPct = est * 100 / limit
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
	if canonical := commandAliases[name]; canonical != "" {
		name = canonical
	}
	m.opts.Usage.Note(name)
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
	if m.hints == nil {
		m.transcript.info("hints are not available in this session")
		return
	}
	switch {
	case len(args) == 0:
		// Whichever way round it is, the line has to name the other
		// way: somebody who has just turned hints off and typed /hints
		// was being told again how to turn them off.
		if m.hints.Off {
			m.transcript.info("hints are off · /hints on brings them back for this session, and they stay off for every project while \"hints\": false is in the config file")
			return
		}
		// Said and spent are different things: a rule suppressed because
		// the first-run line had already proposed it was never a hint
		// anybody read, and counting it as given overstated the help.
		line := plural(len(m.hints.Shown()), "hint") + " shown for this project"
		if n := len(m.hints.Skipped()); n > 0 {
			line += fmt.Sprintf(", %d skipped", n)
		}
		m.transcript.info(line + " · /hints off silences them for this session, and \"hints\": false in the config file does it for every project")
	case args[0] == "on":
		m.hints.Off = false
		m.transcript.info("hints on")
	case args[0] == "off":
		m.hints.Off = true
		m.transcript.info("hints off · \"hints\": false in the config file makes it permanent")
	default:
		m.transcript.errorf("usage: /hints [on|off]")
	}
}

// slowCommandSeconds is how long a foreground command has to hold a turn
// before ctrl+g is worth mentioning.
const slowCommandSeconds = 20

// observeEvent is called from applyEvent for the kinds the hints care
// about. It is a switch over two event kinds, on a path that already
// switches over ten.
func (m *model) observeEvent(ev agent.Event) {
	if m.hints == nil || m.hints.Off {
		return
	}
	if ev.Kind != agent.EvToolResult || ev.Call == nil || ev.Call.Name != "bash" {
		return
	}
	secs := int(ev.Duration.Seconds())
	if secs < slowCommandSeconds || secs <= m.hintState.SlowCommandSeconds {
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
	if m.hints == nil || m.hints.Off {
		return
	}
	cmdline = strings.TrimSpace(cmdline)
	if cmdline == "" || !testCommandRe.MatchString(cmdline) {
		return
	}
	if m.bangSeen == nil {
		m.bangSeen = map[string]int{}
	}
	m.bangSeen[cmdline]++
	if m.bangSeen[cmdline] < 2 {
		return
	}
	m.noteHint(func(s *hints.State) { s.RepeatedBangCommand = cmdline })
}

// observeDenial records a refusal by the rule shape it would have
// saved, which is the shape the r key offers.
func (m *model) observeDenial(shape string) {
	if m.hints == nil || m.hints.Off || shape == "" {
		return
	}
	if m.denials == nil {
		m.denials = map[string]int{}
	}
	m.denials[shape]++
	if m.denials[shape] < 3 {
		return
	}
	m.noteHint(func(s *hints.State) {
		s.DeniedShape, s.DeniedCount = shape, m.denials[shape]
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
	if m.hints == nil || m.hints.Off {
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
