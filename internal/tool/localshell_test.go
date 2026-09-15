package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/shell"
)

func TestLocalShellSkipsSandboxWhenOff(t *testing.T) {
	cmd, err := localShell(context.Background(), nil, nil, t.TempDir(), "echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "docker") {
		t.Fatalf("unwrapped command used docker: %v", cmd.Args)
	}
}

// TestLocalShellRefusesDeniedWrite: a write the active sandbox profile
// forbids is refused while the command is still being built, so the
// model is told why instead of watching a permission error from inside
// the box. The machine may have no sandbox backend at all, which is not
// this test's business.
func TestLocalShellRefusesDeniedWrite(t *testing.T) {
	sb := &shell.Sandbox{}
	if err := sb.SetProfile("workspace"); err != nil {
		t.Fatal(err)
	}
	if err := sb.Enable(); err != nil {
		t.Skipf("no sandbox backend here: %v", err)
	}
	root := t.TempDir()
	if _, err := localShell(context.Background(), nil, sb, root, "rm -rf .janetcode"); err == nil {
		t.Fatal("a write to .janetcode was allowed under the workspace profile")
	}
	if _, err := localShell(context.Background(), nil, sb, root, "go test ./..."); err != nil {
		t.Fatalf("an ordinary command was refused: %v", err)
	}
}
