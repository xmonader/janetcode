package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A language server runs the project's code — rust-analyzer builds
// build.rs and proc macros, others load project plugins — so it gets the
// scrubbed environment every other project-controlled child gets, not
// the agent's provider credentials.
func TestServerDoesNotInheritTheAgentsKey(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-or-v1-should-not-reach-the-server")
	root := t.TempDir()
	dump := filepath.Join(root, "env.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := start(ctx, root, []string{"sh", "-c", `env > "$0"`, dump}, func(string) {})
	if err == nil {
		c.close()
	}
	var data []byte
	for i := 0; i < 100; i++ {
		if data, err = os.ReadFile(dump); err == nil && len(data) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("the server never wrote its environment")
	}
	if strings.Contains(string(data), "OPENROUTER_API_KEY") {
		t.Fatalf("the language server saw the provider key:\n%s", data)
	}
}
