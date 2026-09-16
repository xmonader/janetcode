package mcpcmd

import (
	"context"
	"strings"
	"testing"
)

// git reads a leading dash as one of its own options, and --upload-pack
// names a command to run. "/mcp add --from --upload-pack=…" reached the
// clone as an argument; it does not now.
func TestFromRefusesAnOptionInsteadOfCloningIt(t *testing.T) {
	env := Env{Cwd: t.TempDir(), InstallDir: t.TempDir()}
	for _, from := range []string{
		"--upload-pack=touch /tmp/janetcode-pwned",
		"-u",
		"--exec=sh",
	} {
		_, err := Fetch(context.Background(), env, from)
		if err == nil {
			t.Fatalf("Fetch(%q) was accepted", from)
		}
		if !strings.Contains(err.Error(), "looks like an option") {
			t.Fatalf("Fetch(%q) = %v, want it refused as an option", from, err)
		}
	}
}
