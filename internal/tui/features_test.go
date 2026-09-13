package tui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/slash"
)

var dispatchCase = regexp.MustCompile(`(?m)^\tcase ("/[a-z][a-z0-9?-]*"(?:, "/[a-z][a-z0-9?-]*")*):`)

// TestDispatchedCommandsAreInTheTable reads the source of the command
// dispatcher and checks that every name it answers to is either in the
// command table or a declared alias. The table is what /help, tab
// completion and the catalogue are all built from, so a command that
// only exists in the switch is invisible everywhere else.
func TestDispatchedCommandsAreInTheTable(t *testing.T) {
	src, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (m *model) command(text string) tea.Cmd {")
	if start < 0 {
		t.Fatal("cannot find the command dispatcher; this test needs updating")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find the end of the command dispatcher")
	}
	known := map[string]bool{}
	for _, c := range commands {
		known[c.name] = true
	}
	found := 0
	for _, m := range dispatchCase.FindAllStringSubmatch(body[start:start+end], -1) {
		for _, name := range strings.Split(m[1], ", ") {
			name = strings.Trim(name, `"`)
			found++
			if known[name] || slash.Aliases[name] != "" {
				continue
			}
			t.Errorf("%s is dispatched but is neither in the command table nor a declared alias", name)
		}
	}
	if found < 40 {
		t.Fatalf("only found %d dispatched commands; the regexp has stopped matching", found)
	}
}

// TestHelpTopicFindsTheObviousThings is the behaviour /help <topic> is
// for: a user who knows the problem but not the name.
func TestHelpTopicFindsTheObviousThings(t *testing.T) {
	cat := feature.New()
	cat.AddAll(Features())
	for _, tc := range []struct{ query, want string }{
		{"undo", "/undo"},
		{"remember things across sessions", "/remember"},
		{"tests after every change", "/verify"},
		{"context is full", "/compact"},
		{"command is taking forever", "ctrl+g"},
		{"rsm", "/resume"},
	} {
		hits := cat.Find(tc.query, 5)
		ok := false
		for _, h := range hits {
			if h.Name == tc.want {
				ok = true
			}
		}
		if !ok {
			var got []string
			for _, h := range hits {
				got = append(got, h.Name)
			}
			t.Errorf("%q did not find %s; got %v", tc.query, tc.want, got)
		}
	}
}
