package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	manual "github.com/xmonader/janetcode/docs/manual"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/hints"
)

// flagSet declares the program's flags the same way run() does, so the
// tests below see exactly the flags the program has, not a copy of them.
func flagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	fs, _ := newFlagSet()
	return fs
}

// TestEveryFlagIsCatalogued is the drift gate for the command line. A
// flag with no "when it helps" line cannot be found by anyone who does
// not already know its name.
func TestEveryFlagIsCatalogued(t *testing.T) {
	fs := flagSet(t)
	cat := buildCatalog(fs, nil)
	n := 0
	fs.VisitAll(func(f *flag.Flag) {
		n++
		got, ok := cat.Get(feature.KindFlag, f.Name)
		if !ok {
			t.Errorf("-%s is a flag but not in the catalogue", f.Name)
			return
		}
		if strings.TrimSpace(got.What) == "" {
			t.Errorf("-%s has no description", f.Name)
		}
		if strings.TrimSpace(got.When) == "" {
			t.Errorf("-%s has no \"when it helps\" line: add one to flagWhen in features.go", f.Name)
		}
	})
	if n < 40 {
		t.Fatalf("only %d flags declared; declareFlags has drifted from run()", n)
	}
}

// TestEveryConfigKeyIsCatalogued walks the json tags of the Config
// struct, which is the one list of configuration keys that cannot be
// out of date, and insists each has prose.
func TestEveryConfigKeyIsCatalogued(t *testing.T) {
	cat := buildCatalog(nil, nil)
	keys := configKeys()
	if len(keys) < 20 {
		t.Fatalf("only %d config keys found by reflection", len(keys))
	}
	for _, k := range keys {
		f, ok := cat.Get(feature.KindConfig, k)
		if !ok {
			t.Errorf("config key %q is not in the catalogue", k)
			continue
		}
		if f.What == "" || f.When == "" {
			t.Errorf("config key %q has no description or no \"when\": add one to configWhen in features.go", k)
		}
	}
}

// TestCatalogCoversEveryKind guards against a populator silently
// producing nothing — the failure mode where the catalogue still builds
// but half of janetcode has dropped out of it.
func TestCatalogCoversEveryKind(t *testing.T) {
	cat := buildCatalog(flagSet(t), nil)
	for _, k := range []feature.Kind{feature.KindCommand, feature.KindKey, feature.KindFlag, feature.KindConfig, feature.KindBehave} {
		if len(cat.OfKind(k)) == 0 {
			t.Errorf("no %s features in the catalogue", k)
		}
	}
	if cat.Len() < 120 {
		t.Errorf("catalogue holds only %d features", cat.Len())
	}
}

// TestEveryHintNamesSomethingThatExists is the drift gate on the hint
// texts. A one-time hint is the one moment a user is told a command
// name, and a hint that names a command janetcode does not have — or a
// key that was renamed — is worse than no hint at all, because it is
// spent and never shown again.
func TestEveryHintNamesSomethingThatExists(t *testing.T) {
	cat := buildCatalog(flagSet(t), nil)
	known := map[string]bool{}
	for _, f := range cat.All() {
		known[strings.ToLower(f.Name)] = true
		known[strings.ToLower(f.Invocation())] = true
	}
	// Every rule, with a state contrived to make it fire.
	states := []*hints.State{
		{MutatingTurns: 3},
		{DeniedCount: 3, DeniedShape: "bash(git push *)"},
		{ApprovedCount: 3, ApprovedShape: "bash(go test *)"},
		{Promotable: true, SlowCommandSeconds: 42},
		{ContextPct: 70, Spent: true, CostKnown: true},
		{PasteLines: 80},
		{CostUSD: 1.5},
		{Untrusted: []string{".mcp.json", ".janetcode/hooks.json"}},
		{UnpricedModel: "some/model", Spent: true},
		{ResumedAfterCompaction: true},
		{UndoneMixed: 3},
		{RepeatedTurnShape: "edit_file VERSION, edit_file CHANGELOG.md"},
		{TestFirstPrompt: true},
		{VerifiedAfterFailure: true},
		{ProofAttached: true},
		{SwitchedModel: "claude-opus-5-some-long-name", SessionID: "20260911-153001-4f2a"},
	}
	seen := 0
	for _, r := range hints.Rules() {
		var text string
		for _, s := range states {
			if r.When(s) {
				text = r.Text(s)
				break
			}
		}
		if text == "" {
			t.Errorf("no state in the test makes %s fire", r.ID)
			continue
		}
		seen++
		if r.Feature != "" && !known[strings.ToLower(r.Feature)] {
			t.Errorf("hint %s points at %q, which is not in the catalogue", r.ID, r.Feature)
		}
		for _, word := range strings.Fields(text) {
			word = strings.Trim(word, `"'.,:;()`)
			if !strings.HasPrefix(word, "/") || len(word) < 2 {
				continue
			}
			if strings.Contains(word, "/") && !known[strings.ToLower(word)] {
				t.Errorf("hint %s names %s, which is not a command: %q", r.ID, word, text)
			}
		}
		// One line, and short enough to read in the transcript.
		if strings.Contains(text, "\n") {
			t.Errorf("hint %s is more than one line: %q", r.ID, text)
		}
		if n := len(text); n > 230 {
			t.Errorf("hint %s is %d characters, too long to read as a tip: %q", r.ID, n, text)
		}
	}
	if seen != len(hints.Rules()) {
		t.Errorf("checked %d of %d rules", seen, len(hints.Rules()))
	}
}

// TestEveryFeatureIsInTheManual is the drift gate on the documentation.
// The catalogue is built from the tables the program runs on, so it
// cannot describe a janetcode that does not exist; the manual can go
// quiet about one that does. /agents, /feedback, /todos, --auto-edits,
// --replay and eleven eval flags had all been shipped without a line
// anywhere.
func TestEveryFeatureIsInTheManual(t *testing.T) {
	cat := buildCatalog(flagSet(t), nil)
	var docs strings.Builder
	for _, page := range manual.Pages() {
		body, _ := manual.Page(page)
		docs.WriteString(body + "\n")
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	docs.Write(readme)
	// Line breaks are the manual's own wrapping and must not hide a
	// mention: "click a tool\nline" documents "click a tool line".
	hay := strings.Join(strings.Fields(docs.String()), " ")

	mentions := func(name string) bool {
		i := strings.Index(hay, name)
		for i >= 0 {
			before := byte(' ')
			if i > 0 {
				before = hay[i-1]
			}
			after := byte(' ')
			if j := i + len(name); j < len(hay) {
				after = hay[j]
			}
			if !wordByte(before) && before != '-' && !wordByte(after) && after != '-' {
				return true
			}
			if j := strings.Index(hay[i+1:], name); j >= 0 {
				i += 1 + j
			} else {
				return false
			}
		}
		return false
	}
	for _, f := range cat.All() {
		var names []string
		switch f.Kind {
		case feature.KindCommand:
			names = []string{f.Name}
		case feature.KindKey:
			// A key's Name is sometimes an internal id ("ctrl+o
			// transcript"); what the manual writes is the invocation.
			names = []string{f.Name, f.Invocation()}
		case feature.KindFlag:
			names = []string{"-" + f.Name, "--" + f.Name}
		default:
			continue // tools and config keys are covered by their own pages
		}
		found := false
		for _, n := range names {
			found = found || mentions(n)
		}
		if !found {
			t.Errorf("%s (%s) is in the catalogue and in no document", names[0], f.Kind)
		}
	}
}

func wordByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_'
}

// TestIntrospectAnswersStayInBudget is the leanness gate on the tool:
// whatever is asked, the answer has to fit in what the model was
// promised.
func TestIntrospectAnswersStayInBudget(t *testing.T) {
	cat := buildCatalog(flagSet(t), nil)
	for _, q := range []string{"long output", "undo", "e", "test", "memory across sessions", "context window full", "permissions"} {
		got := feature.Answer(cat, q, "")
		if len(got) > feature.MaxAnswerBytes+120 {
			t.Errorf("answer to %q is %d bytes, over the budget", q, len(got))
		}
		if strings.TrimSpace(got) == "" {
			t.Errorf("answer to %q is empty", q)
		}
	}
}
