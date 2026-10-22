package webui

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFramingIsRestricted: the editor embed hands out a SameSite=None
// cookie, so a page must say who may frame it or any site could.
func TestFramingIsRestricted(t *testing.T) {
	get := func(addr, host string) *httptest.ResponseRecorder {
		h := newServer(context.Background(), Options{Addr: addr}).handler()
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	csp := get("127.0.0.1:8787", "127.0.0.1:8787").Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'self' vscode-webview:") || strings.Contains(csp, "*") {
		t.Errorf("loopback CSP = %q, want frame-ancestors limited to self and the VS Code webview", csp)
	}
	rec := get("0.0.0.0:8787", "devbox.lan:8787")
	if got := rec.Header().Get("Content-Security-Policy"); got != "frame-ancestors 'none'" {
		t.Errorf("remote CSP = %q, want frame-ancestors 'none'", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("remote X-Frame-Options = %q, want DENY", got)
	}
}
