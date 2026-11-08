package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A switch the code reads but the registry does not know is dropped by
// ParseSwitches, so JANETCODE_OFF=<it> silently does nothing: the first
// wraprules and literal A/Bs compared a binary with itself.
func TestEverySwitchReadIsKnown(t *testing.T) {
	re := regexp.MustCompile(`\b[Oo]ff\["([a-z0-9]+)"\]`)
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !KnownSwitch(m[1]) {
				t.Errorf("%s reads switch %q, which KnownSwitch does not list", p, m[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
