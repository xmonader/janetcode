// Package hints says the one useful thing at the one moment it is
// useful, and then never again.
//
// janetcode has features nobody finds. A user who has approved the same
// command nine times does not know that pressing r would have stopped
// it asking; a user watching a test suite hold a turn open for two
// minutes does not know about ctrl+g. A manual does not help, because
// nobody reads a manual at the moment they need it. What helps is a
// line in the transcript at the moment the problem is in front of them.
//
// The cost of that is noise, so the design is entirely about not being
// noisy. A rule fires at most once per project, ever. Only one rule
// fires per evaluation. Nothing fires while the user is answering a
// permission prompt or reading a picker. A feature the user already
// uses is never hinted at. And "hints": false turns the whole thing off.
//
// The engine is a pure function of a State the caller fills in. It has
// no goroutines, no timers and no clock of its own, it reads no files
// while evaluating, and it is called on events — a turn ending, a
// command finishing, a denial — never on a render and never on a
// keystroke.
package hints

import (
	"fmt"
	"sort"
	"strings"
)

// State is everything the rules may look at. The caller updates it as
// things happen; the engine only reads it.
type State struct {
	// Blocked is true while a permission prompt or a picker is open. No
	// hint may interrupt a decision the user is in the middle of.
	Blocked bool

	// MutatingTurns counts turns that changed files.
	MutatingTurns int
	// VerifyConfigured is whether the project has check commands.
	VerifyConfigured bool
	// RepeatedBangCommand is a shell command the user has run twice
	// themselves with !, when it looks like a test or build command.
	RepeatedBangCommand string

	// SlowCommand is the longest foreground command of the session, in
	// seconds, and what it was.
	SlowCommandSeconds int
	SlowCommand        string
	// Promotable is whether there is in fact a foreground command that
	// ctrl+g could move. Hinting at a key that would do nothing is worse
	// than saying nothing.
	Promotable bool

	// DeniedShape is a permission rule shape the user has refused three
	// times, and how often.
	DeniedShape string
	DeniedCount int

	// ApprovedShape is a permission rule shape the ledger has seen
	// approved perm.LearnAfter times with no refusal, and how often. It
	// is what the r key, or /permissions allow, would save.
	ApprovedShape string
	ApprovedCount int

	// ContextPct is how full the context is against the compaction
	// threshold.
	ContextPct int

	// PasteLines is the size of the last paste, and PasteLooksLikeTrace
	// whether it looked like a stack trace or a diagnostic dump.
	PasteLines          int
	PasteLooksLikeTrace bool

	// CostUSD is what the session has spent.
	CostUSD float64

	// Untrusted names project files that are present but not enabled:
	// ".mcp.json", ".janetcode/skills", ".janetcode/hooks.json".
	Untrusted []string

	// UnpricedModel is the model name when it was chosen by the
	// environment and no price table knows it.
	UnpricedModel string
	// Spent is whether the session has bought anything yet, and
	// CostKnown whether a figure came out of it. A price table is not
	// the only source of one: most providers report the cost of a call
	// with the call. So the hint that says the figures will be blank
	// waits until there has been a call and they really are.
	Spent, CostKnown bool

	// ResumedAfterCompaction is set when this session was resumed and the
	// history it came back with had already been summarised.
	ResumedAfterCompaction bool

	// RepeatedTurnShape describes the file changes of a turn that
	// repeated, in order and on the same files, those of an earlier turn
	// of the session: what /recipe save would have kept.
	RepeatedTurnShape string

	// UndoneMixed is how many distinct files or intents a whole-turn
	// /undo just reverted together, when that was more than one: the
	// case /undo about reverts only part of.
	UndoneMixed int

	// Queued is set the first time the user types a message while a turn
	// is running and presses enter, which queues it for afterwards
	// rather than handing it to the turn in front of them.
	Queued bool

	// TestFirstPrompt is set when the user asks for tests to be written,
	// or for a large feature, in a prompt: what /accept is for.
	TestFirstPrompt bool

	// ProofAttached is set when the session's own commit has had a proof
	// note attached.
	ProofAttached bool

	// VerifiedAfterFailure is set when a turn ended verified: a check
	// that had been failing now passes.
	VerifiedAfterFailure bool

	// SwitchedModel is the model the user switched to after at least one
	// turn on another, and SessionID the session --what-if would replay.
	SwitchedModel string
	SessionID     string

	// Used reports whether the user has already used a catalogue
	// feature. A rule pointing at a feature the user knows is spent
	// silently rather than said, and the walk carries on to the next
	// rule; the order of the rules themselves is fixed (see Rules).
	// Nil means "unknown", which is treated as unused.
	Used func(feature string) bool
}

// Rule is one hint: what has to be true, what to say, and which feature
// it is trying to introduce.
type Rule struct {
	// ID is stable and is what gets recorded once the rule has fired.
	// Renaming one makes it fire again for everybody, so do not.
	ID string
	// Feature is the catalogue name the hint points at, used to skip
	// rules for features the user already knows.
	Feature string
	// Use is the usage key that means the user already knows what the
	// hint teaches, when that is narrower than Feature: a subcommand
	// such as "/undo about", where having typed /undo says nothing.
	// Empty means Feature.
	Use string
	// When decides whether the rule applies right now.
	When func(*State) bool
	// Text is the hint. Present tense, one line, names what to type.
	Text func(*State) string
}

func (r Rule) usage() string {
	if r.Use != "" {
		return r.Use
	}
	return r.Feature
}

// Engine holds the rules and what has already been said.
//
// It is not safe for concurrent use and does not need to be: the
// terminal evaluates on its own event loop, one event at a time, and
// every field here is read and written from there.
type Engine struct {
	Rules []Rule
	// Off disables every hint ("hints": false, or /hints off).
	Off bool

	shown map[string]bool
	// skipped are the rules that were spent without being said. They
	// suppress the rule as firmly as a shown one; they are kept apart
	// so that /hints can report what was said rather than what was
	// merely used up.
	skipped map[string]bool
	// Record persists that a hint has been given. Errors are ignored:
	// the worst case of a failed write is one repeated hint.
	Record func(id string)
}

// New builds an engine over the default rules, with the ids that have
// already fired in this project: the ones that were said, and the ones
// that were spent without being said.
func New(shown, skipped []string) *Engine {
	e := &Engine{Rules: Rules(), shown: map[string]bool{}, skipped: map[string]bool{}}
	for _, id := range shown {
		e.shown[id] = true
	}
	for _, id := range skipped {
		e.skipped[id] = true
	}
	return e
}

// Shown lists the ids that were said, for persisting and for counting.
func (e *Engine) Shown() []string { return sortedIDs(e.shown) }

// Skipped lists the ids that were spent without being said.
func (e *Engine) Skipped() []string { return sortedIDs(e.skipped) }

func sortedIDs(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for id := range m {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Eval returns the one hint to show now, or "" for silence. A rule that
// fires is marked as shown before the text is returned, so a caller that
// drops the string still does not see the hint twice.
func (e *Engine) Eval(s *State) string {
	if e == nil || e.Off || s == nil || s.Blocked {
		return ""
	}
	for _, r := range e.Rules {
		if e.spent(r.ID) || r.When == nil || r.Text == nil || !r.When(s) {
			continue
		}
		// A feature the user already reaches for needs no introduction.
		// The rule is still marked as fired: the moment it belonged to
		// has passed, and offering it later would be worse than not.
		if s.Used != nil && r.usage() != "" && s.Used(r.usage()) {
			e.fire(r.ID, false)
			continue
		}
		e.fire(r.ID, true)
		return r.Text(s)
	}
	return ""
}

// Silent reports whether hints are turned off, for the one line the
// terminal prints itself: somebody who asked for quiet meant this too.
func (e *Engine) Silent() bool { return e != nil && e.Off }

// Given reports whether an id has already fired in this project, said
// or skipped. It is for the one line the terminal says itself rather
// than through a rule — the first-run project line — which has to be as
// once-only as everything else here.
func (e *Engine) Given(id string) bool {
	return e != nil && e.spent(id)
}

// MarkShown records an id the terminal said itself, so the rule that
// would say it again is spent and the count of hints given includes it.
func (e *Engine) MarkShown(id string) {
	if e == nil || id == "" {
		return
	}
	e.fire(id, true)
}

// MarkSpent spends a rule without saying anything, for a caller that has
// already covered the same ground some other way. The first-run line
// proposes the verification command, so the rule that would propose it
// again is spent there rather than left to repeat it three turns later —
// but nobody was shown a hint, and /hints must not claim they were.
func (e *Engine) MarkSpent(id string) {
	if e == nil || id == "" {
		return
	}
	e.fire(id, false)
}

func (e *Engine) spent(id string) bool { return e.shown[id] || e.skipped[id] }

func (e *Engine) fire(id string, said bool) {
	if e.shown == nil {
		e.shown = map[string]bool{}
	}
	if e.skipped == nil {
		e.skipped = map[string]bool{}
	}
	if said {
		e.shown[id] = true
	} else {
		e.skipped[id] = true
	}
	if e.Record != nil {
		e.Record(id)
	}
}

// Rules are the hints janetcode ships with, in the order they are
// considered. The order is the priority: an unanswered permission
// prompt matters more than a note about prices.
func Rules() []Rule {
	return []Rule{
		{
			ID: "verify-gate", Feature: "/verify",
			When: func(s *State) bool {
				return !s.VerifyConfigured && (s.MutatingTurns >= 3 || s.RepeatedBangCommand != "")
			},
			Text: func(s *State) string {
				cmd := s.RepeatedBangCommand
				if cmd == "" {
					cmd = "go test ./..."
				}
				return fmt.Sprintf("/verify set %s runs it after every turn that changes code and hands failures straight back to the model, so you do not have to", cmd)
			},
		},
		{
			// The rule fires on refusals, so it must offer a refusal.
			// It used to offer the r key, which saves an *allow* rule:
			// the advice after three denials was to let the thing
			// through for ever.
			ID: "permission-rule", Feature: "/permissions",
			When: func(s *State) bool { return s.DeniedCount >= 3 && s.DeniedShape != "" },
			Text: func(s *State) string {
				return fmt.Sprintf("that is the %d%s time you have refused %s — /permissions deny %q refuses calls of that shape from now on without asking you again, and /permissions lists what is saved", s.DeniedCount, ordinal(s.DeniedCount), s.DeniedShape, s.DeniedShape)
			},
		},
		{
			// Fires the first time a shape crosses the learn threshold
			// with a clean record; a refusal after that never lets it
			// fire, since permission-rule above is the honest advice then.
			ID: "learned-rule", Feature: "/permissions",
			When: func(s *State) bool { return s.ApprovedCount >= 3 && s.ApprovedShape != "" },
			Text: func(s *State) string {
				return fmt.Sprintf("you have approved %s %d times running and never refused it — r at the next prompt saves it as a rule, or /permissions allow %q does it now", s.ApprovedShape, s.ApprovedCount, s.ApprovedShape)
			},
		},
		{
			ID: "background-slow", Feature: "ctrl+g",
			When: func(s *State) bool { return s.Promotable && s.SlowCommandSeconds >= 20 },
			Text: func(s *State) string {
				// Said after the command finished, so it has to read as
				// advice for next time: "ctrl+g moves the running
				// command" invites somebody to press it now, at an idle
				// prompt, where it does nothing.
				return fmt.Sprintf("that command held the turn for %ds — while the next one is still running, ctrl+g hands it to the background, where it finishes as a job (/jobs shows it) and the turn carries on without waiting", s.SlowCommandSeconds)
			},
		},
		{
			ID: "context-filling", Feature: "/compact",
			// Not before the conversation exists. With a low compact_at
			// the system prompt alone can be 70% of the threshold, and
			// the first thing a fresh session said was "the context is
			// 70% full · /compact summarises it now" — advice about a
			// conversation the user had not started.
			When: func(s *State) bool { return s.ContextPct >= 60 && s.Spent },
			Text: func(s *State) string {
				return fmt.Sprintf("the context is %d%% full — /context shows what is filling it, /compact summarises it now rather than mid-task. Either way nothing is lost: the exact text of every tool result stays in the archive the model reads back", s.ContextPct)
			},
		},
		{
			ID: "paste-as-file", Feature: "@path",
			When: func(s *State) bool { return s.PasteLines >= 40 || s.PasteLooksLikeTrace },
			Text: func(s *State) string {
				what := fmt.Sprintf("that was a %d-line paste", s.PasteLines)
				if s.PasteLooksLikeTrace {
					what = "that looked like a stack trace"
				}
				return what + " — @path attaches a file instead, which costs a reference rather than the whole text, and tab completes the path"
			},
		},
		{
			ID: "undo-about", Feature: "/undo", Use: "/undo about",
			When: func(s *State) bool { return s.UndoneMixed >= 2 },
			Text: func(s *State) string {
				return fmt.Sprintf("that reverted %d separate changes at once — when only part of a turn is wrong, /undo about <description> previews just the matching changes and /undo apply reverts those", s.UndoneMixed)
			},
		},
		{
			ID: "recipe-repeat", Feature: "/recipe", Use: "/recipe save",
			When: func(s *State) bool { return s.RepeatedTurnShape != "" },
			Text: func(s *State) string {
				return "this turn made the same changes as an earlier one (" + s.RepeatedTurnShape + ") — /recipe save <name> keeps them as a program that /recipe run <name> repeats with no model call"
			},
		},
		{
			// Enter and ctrl+s are one keystroke apart and do opposite
			// things with the same text. The box names both while a turn
			// runs; this says it once, in the transcript, at the moment
			// somebody has just queued something and may well have meant
			// the running turn to hear it. Feature is ctrl+s, so anyone
			// who already steers is never told.
			ID: "steer-or-queue", Feature: "ctrl+s",
			When: func(s *State) bool { return s.Queued },
			Text: func(_ *State) string {
				return "that message is queued and goes out when this turn ends — ctrl+s instead hands what you have typed to the turn that is running, which reads it before its next step; ↑ takes the queued one back"
			},
		},
		{
			ID: "accept-tests", Feature: "/accept",
			When: func(s *State) bool { return s.TestFirstPrompt },
			Text: func(_ *State) string {
				return "/accept <request> has the tests written first and stops for you to read them; /accept approve then locks them, so the code is written against tests nothing can weaken"
			},
		},
		{
			ID: "eval-keep", Feature: "/eval", Use: "/eval keep",
			When: func(s *State) bool { return s.VerifiedAfterFailure },
			Text: func(_ *State) string {
				return "that turn fixed tests that had been failing — /eval keep <name> saves it as a check janetcode --eval can re-run, so the fix stays fixed"
			},
		},
		{
			ID: "proof-notes", Feature: "/proof",
			When: func(s *State) bool { return s.ProofAttached },
			Text: func(_ *State) string {
				return "that commit carries a proof note of which turn wrote each file and what showed it worked — /proof shows it, and /proof push shares the notes, which stay in this clone until pushed"
			},
		},
		{
			ID: "what-if-model", Feature: "what-if",
			When: func(s *State) bool { return s.SwitchedModel != "" && s.SessionID != "" },
			Text: func(s *State) string {
				return fmt.Sprintf("janetcode --what-if %s --from-turn 1 --model %s replays this session's prompts on %s in a throwaway worktree and compares the outcomes", s.SessionID, s.SwitchedModel, s.SwitchedModel)
			},
		},
		{
			ID: "spend", Feature: "/budget",
			When: func(s *State) bool { return s.CostUSD >= 0.50 },
			Text: func(s *State) string {
				return fmt.Sprintf("this session has spent $%.2f. /budget <usd> stops the session at a ceiling, /usage breaks the figure down, and a headless run takes --max-cost", s.CostUSD)
			},
		},
		{
			// Only when there is a command to name: the hint's whole
			// point is what to type, and an entry nothing here knows
			// about used to produce a line ending in a bare colon.
			ID: "trust-project", Feature: "/permissions",
			When: func(s *State) bool { return anyTrustCommand(s.Untrusted) },
			Text: func(s *State) string {
				return "this project ships " + list(s.Untrusted) + ", which " + verb(len(s.Untrusted)) + " waiting for your approval: " + trustCommands(s.Untrusted)
			},
		},
		{
			ID: "unpriced-model", Feature: "prices",
			When: func(s *State) bool { return s.UnpricedModel != "" && s.Spent && !s.CostKnown },
			Text: func(s *State) string {
				return "nothing can price " + s.UnpricedModel + ", so the cost figures will be blank rather than wrong. Add it under \"prices\" in the config file (input and output per million tokens) and /usage starts reporting"
			},
		},
		{
			ID: "resumed-compacted", Feature: "/context",
			When: func(s *State) bool { return s.ResumedAfterCompaction },
			Text: func(_ *State) string {
				return "this session had been summarised before you left it, so the model has that summary and the recent turns rather than the whole conversation — /context shows what came back"
			},
		},
	}
}

func ordinal(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return "th"
	case n%10 == 1:
		return "st"
	case n%10 == 2:
		return "nd"
	case n%10 == 3:
		return "rd"
	}
	return "th"
}

func list(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func verb(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// trustable maps what an untrusted path is recognised by to the command
// that enables it, in the order the markers are tried. A table rather
// than a chain of conditions: one line adds a kind, and trustCommands
// and anyTrustCommand cannot drift apart.
var trustable = []struct{ marker, command string }{
	{"mcp", "/mcp trust"},
	{"hooks", "/hooks trust"},
	{"permissions", "/permissions trust"},
	{"verify", "/verify trust"},
	{"skills", "/skills lists them"},
}

// trustCommand names the command that enables one untrusted thing.
func trustCommand(path string) (string, bool) {
	for _, t := range trustable {
		if strings.Contains(path, t.marker) {
			return t.command, true
		}
	}
	return "", false
}

// anyTrustCommand reports whether anything in xs has a command, without
// building a string: it is on the evaluation path, which allocates
// nothing until a rule actually fires.
func anyTrustCommand(xs []string) bool {
	for _, x := range xs {
		if _, ok := trustCommand(x); ok {
			return true
		}
	}
	return false
}

// trustCommands names the command for each untrusted thing. Project
// files are not enabled by being present, so the hint has to say what
// enables them.
func trustCommands(xs []string) string {
	var out []string
	for _, x := range xs {
		if cmd, ok := trustCommand(x); ok {
			out = append(out, cmd)
		}
	}
	return list(dedupe(out))
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
