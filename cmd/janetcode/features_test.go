package main

import (
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/feature"
)

// flagSet runs the cheapest command there is so that run() declares its
// flags, then takes the flag set it published. The tests below therefore
// see exactly the flags the program has, not a copy of them.
func flagSet(t *testing.T) *flag.FlagSet {
	t.Helper()
	stdout := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = devnull
	err = run([]string{"-version"})
	os.Stdout = stdout
	devnull.Close()
	if err != nil {
		t.Fatalf("run -version: %v", err)
	}
	fs := declaredFlags.Load()
	if fs == nil {
		t.Fatal("run() published no flag set")
	}
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
