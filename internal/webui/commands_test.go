package webui

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/slash"
)

var dispatchCase = regexp.MustCompile(`(?m)^\tcase ("/[a-z][a-z0-9?-]*"(?:, "/[a-z][a-z0-9?-]*")*):`)

// TestDispatchedCommandsAreInTheTable reads the source of runCommand, the
// web front-end's command dispatcher, and checks that every name it
// answers to is either in webCommands or a declared alias. webCommands is
// what the palette, /help and the commands API are all built from, so a
// command that only exists in the switch is invisible everywhere else. It
// mirrors internal/tui/features_test.go's test of the same name for the
// terminal dispatcher.
func TestDispatchedCommandsAreInTheTable(t *testing.T) {
	src, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (s *server) runCommand(text string) error {")
	if start < 0 {
		t.Fatal("cannot find the command dispatcher; this test needs updating")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("cannot find the end of the command dispatcher")
	}
	known := map[string]bool{}
	for _, name := range webCommands {
		known[name] = true
	}
	dispatched := map[string]bool{}
	found := 0
	for _, m := range dispatchCase.FindAllStringSubmatch(body[start:start+end], -1) {
		for _, name := range strings.Split(m[1], ", ") {
			name = strings.Trim(name, `"`)
			found++
			dispatched[name] = true
			if known[name] || slash.Aliases[name] != "" {
				continue
			}
			t.Errorf("%s is dispatched but is neither in webCommands nor a declared alias", name)
		}
	}
	if found < 40 {
		t.Fatalf("only found %d dispatched commands; the regexp has stopped matching", found)
	}
	for _, name := range webCommands {
		if !dispatched[name] {
			t.Errorf("%s is in webCommands but runCommand never dispatches it", name)
		}
	}
}
