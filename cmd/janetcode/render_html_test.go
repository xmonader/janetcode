package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/tool"
)

// render_html is registered on every agent, and its pages land beside the
// real session file with workspace images embedded.
func TestRenderHTMLIsRegisteredAndStoresBesideTheSession(t *testing.T) {
	b, here, _ := cdBuild(t)
	rt, ok := b.agent.Tools.Get("render_html")
	if !ok {
		t.Fatal("render_html is not registered")
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(here, "logo.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]string{"title": "Logo", "html": `<h1>hi</h1><img src="logo.png">`})
	out, err := rt.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	line, ok := tool.PageResultLine(out)
	if !ok {
		t.Fatalf("result has no card: %q", out)
	}
	path := line[strings.LastIndex(line, " · ")+len(" · "):]
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saved page missing: %v", err)
	}
	if !strings.Contains(string(data), "data:image/png;base64,") || strings.Contains(string(data), `src="logo.png"`) {
		t.Errorf("image not embedded in stored page: %s", data)
	}
	if !strings.Contains(filepath.Base(path), ".page-1.html") {
		t.Errorf("page path %q is not a session sidecar", path)
	}

	bad, _ := json.Marshal(map[string]string{"title": "x", "html": `<img src="/etc/passwd.png">`})
	if _, err := rt.Run(context.Background(), bad); err == nil {
		t.Error("an image outside the workspace was accepted")
	}
}
