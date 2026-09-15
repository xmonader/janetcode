package tool

import (
	"context"
	"strings"
	"testing"
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
