package openai

import (
	"context"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/netcfg"
)

// The provider built its own bare http.Transport, so a CA added for a
// TLS-intercepting proxy reached every client but this one: the provider
// refused the proxy's certificate while fetch_url went through.
func TestProviderTrustsTheConfiguredCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := netcfg.AddCAFile(ca); err != nil {
		t.Fatal(err)
	}
	p, err := New(Config{APIKey: "k", BaseURL: srv.URL + "/v1", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", MaxTokens: 5, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("stream through the private CA: %v", err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
}
