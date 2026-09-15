package feature

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/slash"
)

// TestEveryCommandIsCatalogued is the drift gate. A command the user can
// type but that the catalogue cannot describe is a feature nobody can
// find: /help <topic> will not mention it, --inspect features will not
// list it, and the model will not know it exists. Adding a command
// therefore has to mean adding a line saying when it helps.
func TestEveryCommandIsCatalogued(t *testing.T) {
	cat := New()
	cat.AddAll(Commands())
	for _, c := range slash.Commands {
		f, ok := cat.Get(KindCommand, c.Name)
		if !ok {
			t.Errorf("%s is in the command table but not in the catalogue", c.Name)
			continue
		}
		if strings.TrimSpace(f.What) == "" {
			t.Errorf("%s has no description", c.Name)
		}
		if strings.TrimSpace(f.When) == "" {
			t.Errorf("%s has no \"when it helps\" line: add one to commandWhen in commands.go", c.Name)
		}
	}
}
