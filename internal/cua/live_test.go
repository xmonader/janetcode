package cua

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
)

// TestDriverMCPHandshake is the live proof that janetcode can talk to an
// installed Cua Driver. Skipped when none is on PATH.
func TestDriverMCPHandshake(t *testing.T) {
	d, ok := Detect()
	if !ok {
		t.Skip("no cua-driver or open-computer-use on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := mcp.Start(ctx, ServerName, d.Config(), mcp.Options{Root: t.TempDir(), HandshakeTimeout: 20 * time.Second})
	if err != nil {
		t.Fatalf("mcp start %s: %v", d.Path, err)
	}
	defer c.Close()
	var names []string
	for _, tool := range c.Tools() {
		names = append(names, tool.Name)
	}
	joined := strings.Join(names, ",")
	t.Logf("%s %s: %d tools: %s", d.Kind, d.Path, len(names), joined)
	if len(names) == 0 {
		t.Fatal("driver MCP offered no tools")
	}
	if !strings.Contains(joined, "list_apps") && !strings.Contains(joined, "screenshot") && !strings.Contains(joined, "click") {
		t.Fatalf("driver tools do not look like computer-use: %s", joined)
	}
	res, err := c.CallTool(ctx, "list_apps", nil)
	if err != nil {
		t.Fatalf("list_apps: %v", err)
	}
	if !strings.Contains(res.Text, "app") && !strings.Contains(res.Text, "pid") {
		t.Fatalf("list_apps did not look like an app list: %q", clip(res.Text, 200))
	}
	t.Logf("list_apps ok (%d bytes)", len(res.Text))
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
