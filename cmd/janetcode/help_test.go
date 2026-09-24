package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

func helpText(t *testing.T) string {
	t.Helper()
	fs, _ := newFlagSet()
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	fs.Usage()
	return buf.String()
}

// Every flag shows up in --help exactly once, whichever section it is
// filed in, so grouping can never hide a flag or print it twice.
func TestHelpListsEveryFlagOnce(t *testing.T) {
	help := helpText(t)
	fs, _ := newFlagSet()
	fs.VisitAll(func(f *flag.Flag) {
		dash := "--"
		if len(f.Name) == 1 {
			dash = "-"
		}
		entry := "\n  " + dash + f.Name
		n := strings.Count(help+"\n", entry+" ") + strings.Count(help, entry+"\n")
		if n != 1 {
			t.Errorf("%s%s appears %d times in --help, want 1", dash, f.Name, n)
		}
	})
}

// A renamed or deleted flag must not linger in helpGroups: it would
// silently vanish from its section and the list would lie.
func TestHelpGroupsNameRealFlags(t *testing.T) {
	fs, _ := newFlagSet()
	for _, g := range helpGroups {
		for _, n := range g.names {
			if fs.Lookup(n) == nil {
				t.Errorf("helpGroups %q names %q, which is not a flag", g.title, n)
			}
		}
	}
}

// The everyday flags come first and the eval knobs last, so a new user
// reads --model and -p before --eval-facts-blind.
func TestHelpSectionsOrderEverydayFirst(t *testing.T) {
	help := helpText(t)
	order := []string{"\nSession:", "  --model", "\nHeadless (-p):", "  -p ", "\nPermissions:", "\nAdvanced:", "\nEval and benchmark", "  --eval ", "\nEnvironment:"}
	last := -1
	for _, s := range order {
		i := strings.Index(help, s)
		if i < 0 {
			t.Fatalf("--help lacks %q", s)
		}
		if i < last {
			t.Errorf("%q is out of order in --help", s)
		}
		last = i
	}
}
