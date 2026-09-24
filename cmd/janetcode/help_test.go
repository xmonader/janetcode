package main

import (
	"bytes"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
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

// A mistyped flag is one line in the help's spelling with a way
// forward, and a usage error (exit 42) -- not 170 lines of help and the
// flag package's wording, exiting 1.
func TestFlagErrorsAreOneLineUsageErrors(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--bogus"}, "unknown flag --bogus (janetcode --help lists them)"},
		{[]string{"-x"}, "unknown flag -x (janetcode --help lists them)"},
		{[]string{"--max-cost", "abc"}, `--max-cost: "abc" is not a number`},
		{[]string{"--eval-timeout", "soon"}, `--eval-timeout: "soon" is not a duration like 90s or 5m`},
		{[]string{"--yolo=maybe"}, `--yolo: "maybe" is not true or false`},
		{[]string{"--model"}, "--model needs a value"},
	} {
		s := &startup{args: c.args}
		var err error
		out := captureStderr(t, func() { _, err = s.stageFlags() })
		if err == nil || err.Error() != c.want {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.want)
		}
		if headless.ExitCode(err) != headless.ExitBadInput {
			t.Errorf("%v: exit %d, want %d", c.args, headless.ExitCode(err), headless.ExitBadInput)
		}
		if out != "" {
			t.Errorf("%v: printed %d bytes besides the error:\n%s", c.args, len(out), out)
		}
	}
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = old }()
	f()
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}
