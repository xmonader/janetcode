package sdk_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp/testserver"
	"github.com/xmonader/janetcode/sdk"
)

func init() {
	// The test binary doubles as a stdio MCP server.
	if os.Getenv("SDK_FAKE_MCP") == "1" {
		testserver.Run()
		os.Exit(0)
	}
}

// An ACP session has the MCP servers of janetcode's own configuration and
// the ones the client passes in session/new: before, --acp started none,
// so an SDK or --mcp-serve session had no MCP tools at all.
func TestSessionHasConfiguredAndClientMCPServers(t *testing.T) {
	bin := janetcode(t)
	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	env := isolatedEnv(t, srv.URL)
	cfgDir := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "XDG_CONFIG_HOME="); ok {
			cfgDir = filepath.Join(v, "janetcode")
		}
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcp_servers": {"cfgsrv": {"command": "` + os.Args[0] + `", "env": {"SDK_FAKE_MCP": "1"}}}}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := sdk.Run(ctx, sdk.Options{
		Binary: bin, Dir: t.TempDir(), Provider: "openai", Model: "test-model", Env: env,
		MCPServers: []sdk.MCPServer{{Name: "edsrv", Command: os.Args[0], Env: map[string]string{"SDK_FAKE_MCP": "1"}}},
	}, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "end_turn" {
		t.Fatalf("result = %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(bodies, "\n")
	for _, want := range []string{"mcp__cfgsrv__", "mcp__edsrv__"} {
		if !strings.Contains(all, want) {
			t.Errorf("the model was never offered %s* tools", want)
		}
	}
}
