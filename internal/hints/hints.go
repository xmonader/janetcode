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
	// environment and nothing can price it, so the cost figures are
	// missing rather than wrong.
	UnpricedModel string

	// ResumedAfterCompaction is set when this session was resumed and the
	// history it came back with had already been summarised.
	ResumedAfterCompaction bool

	// Used reports whether the user has already used a catalogue
	// feature. Rules that point at a feature the user knows are skipped,
	// and among the rest the unused ones go first. Nil means "unknown",
	// which is treated as unused.
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
	// When decides whether the rule applies right now.
	When func(*State) bool
	// Text is the hint. Present tense, one line, names what to type.
	Text func(*State) string
}

// Engine holds the rules and what has already been said.
type Engine struct {
	Rules []Rule
	// Off disables every hint ("hints": false, or /hints off).
	Off bool

	shown map[string]bool
	// Record persists that a hint has been given. Errors are ignored:
	// the worst case of a failed write is one repeated hint.
	Record func(id string)
}

// New builds an engine over the default rules, with the ids that have
// already fired in this project.
func New(shown []string) *Engine {
	e := &Engine{Rules: Rules(), shown: map[string]bool{}}
	for _, id := range shown {
		e.shown[id] = true
	}
	return e
}

// Shown lists the ids that have fired, for persisting.
func (e *Engine) Shown() []string {
	out := make([]string, 0, len(e.shown))
	for id := range e.shown {
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
		if e.shown[r.ID] || r.When == nil || r.Text == nil || !r.When(s) {
			continue
		}
		// A feature the user already reaches for needs no introduction.
		// The rule is still marked as fired: the moment it belonged to
		// has passed, and offering it later would be worse than not.
		if s.Used != nil && r.Feature != "" && s.Used(r.Feature) {
			e.fire(r.ID)
			continue
		}
		e.fire(r.ID)
		return r.Text(s)
	}
	return ""
}

// MarkShown spends a rule without saying anything, for a caller that
// has already said it some other way. The first-run line proposes the
// verification command, so the rule that would propose it again is
// spent there rather than left to repeat it three turns later.
func (e *Engine) MarkShown(id string) {
	if e == nil || id == "" {
		return
	}
	e.fire(id)
}

func (e *Engine) fire(id string) {
	if e.shown == nil {
		e.shown = map[string]bool{}
	}
	e.shown[id] = true
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
				return fmt.Sprintf("/verify set %q runs it after every turn that changes code and hands failures straight back to the model, so you do not have to. It is saved in .janetcode/verify.json", cmd)
			},
		},
		{
			ID: "permission-rule", Feature: "r",
			When: func(s *State) bool { return s.DeniedCount >= 3 && s.DeniedShape != "" },
			Text: func(s *State) string {
				return fmt.Sprintf("that is the %d%s time you have refused %s — pressing r at the prompt saves a rule so calls of that shape never ask again, and /permissions lists what is saved", s.DeniedCount, ordinal(s.DeniedCount), s.DeniedShape)
			},
		},
		{
			ID: "background-slow", Feature: "ctrl+g",
			When: func(s *State) bool { return s.Promotable && s.SlowCommandSeconds >= 20 },
			Text: func(s *State) string {
				return fmt.Sprintf("that command held the turn for %ds — ctrl+g moves a running foreground command to the background, where it keeps going as a job (/jobs) while the turn carries on", s.SlowCommandSeconds)
			},
		},
		{
			ID: "context-filling", Feature: "/compact",
			When: func(s *State) bool { return s.ContextPct >= 60 },
			Text: func(s *State) string {
				return fmt.Sprintf("the context is %d%% full. It will be summarised automatically before it runs out: the recent turns are kept whole and the exact text of every tool result stays in the archive, which the model reads back with expand_output. /context shows what is filling it and /compact does it now rather than mid-task", s.ContextPct)
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
			ID: "spend", Feature: "/budget",
			When: func(s *State) bool { return s.CostUSD >= 0.50 },
			Text: func(s *State) string {
				return fmt.Sprintf("this session has spent $%.2f. /budget <usd> stops the session at a ceiling, /usage breaks the figure down, and a headless run takes --max-cost", s.CostUSD)
			},
		},
		{
			ID: "trust-project", Feature: "/permissions",
			When: func(s *State) bool { return len(s.Untrusted) > 0 },
			Text: func(s *State) string {
				return "this project ships " + list(s.Untrusted) + ", which " + verb(len(s.Untrusted)) + " waiting for your approval: " + trustCommands(s.Untrusted)
			},
		},
		{
			ID: "unpriced-model", Feature: "prices",
			When: func(s *State) bool { return s.UnpricedModel != "" },
			Text: func(s *State) string {
				return "nothing can price " + s.UnpricedModel + ", so the cost figures will be blank rather than wrong. Add it under \"prices\" in the config file (input and output per million tokens) and /usage starts reporting"
			},
		},
		{
			ID: "resumed-compacted", Feature: "/context",
			When: func(s *State) bool { return s.ResumedAfterCompaction },
			Text: func(_ *State) string {
				return "this session was summarised before you left it, so the model has the summary and the recent turns rather than the whole conversation. /context shows what came back, and the exact text of the old tool results is still in the archive"
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

// trustCommands names the command for each untrusted thing. Project
// files are not enabled by being present, so the hint has to say what
// enables them.
func trustCommands(xs []string) string {
	var out []string
	for _, x := range xs {
		switch {
		case strings.Contains(x, "mcp"):
			out = append(out, "/mcp trust")
		case strings.Contains(x, "hooks"):
			out = append(out, "/hooks trust")
		case strings.Contains(x, "permissions"):
			out = append(out, "/permissions trust")
		case strings.Contains(x, "verify"):
			out = append(out, "/verify trust")
		case strings.Contains(x, "skills"):
			out = append(out, "/skills lists them")
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
