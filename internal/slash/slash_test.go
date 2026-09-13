package slash

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/feature"
)

// TestEveryCommandIsCatalogued is the drift gate. A command the user can
// type but that the catalogue cannot describe is a feature nobody can
// find: /help <topic> will not mention it, --inspect features will not
// list it, and the model will not know it exists. Adding a command
// therefore has to mean adding a line saying when it helps.
func TestEveryCommandIsCatalogued(t *testing.T) {
	cat := feature.New()
	cat.AddAll(Features())
	for _, c := range Commands {
		f, ok := cat.Get(feature.KindCommand, c.Name)
		if !ok {
			t.Errorf("%s is in the command table but not in the catalogue", c.Name)
			continue
		}
		if strings.TrimSpace(f.What) == "" {
			t.Errorf("%s has no description", c.Name)
		}
		if strings.TrimSpace(f.When) == "" {
			t.Errorf("%s has no \"when it helps\" line: add one to When in features.go", c.Name)
		}
	}
}
