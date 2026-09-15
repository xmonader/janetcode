package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/weburl"
)

const ddgPage = `<html><body><div id="links">
<div class="result results_links results_links_deep web-result">
 <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&amp;rut=abc">Go <b>Documentation</b></a></h2>
 <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F">The Go programming language &amp; its docs.</a>
</div>
<div class="result results_links results_links_deep web-result">
 <h2 class="result__title"><a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=bing&amp;u3=x">Sponsored thing</a></h2>
 <a class="result__snippet">buy now</a>
</div>
<div class="result">
 <h2 class="result__title"><a class="result__a" href="https://pkg.go.dev/net/http">net/http package</a></h2>
</div>
<div class="result">
 <h2 class="result__title"><a class="result__a" href="https://example.org/three">Three</a></h2>
 <a class="result__snippet">third</a>
</div>
</div></body></html>`

func TestWebSearchDuckDuckGo(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(ddgPage))
	}))
	defer srv.Close()
	ws := WebSearch{Config: weburl.SearchConfig{URL: srv.URL + "/html/"}}
	out, err := ws.Run(context.Background(), json.RawMessage(`{"query":"go net/http docs","max_results":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "go net/http docs" {
		t.Fatalf("query sent = %q", gotQuery)
	}
	for _, want := range []string{"2 result(s)", "1. Go Documentation\n   https://go.dev/doc/\n   The Go programming language & its docs.", "2. net/http package\n   https://pkg.go.dev/net/http"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Sponsored") || strings.Contains(out, "Three") {
		t.Fatalf("ad kept or limit ignored:\n%s", out)
	}
	if !strings.Contains(ws.Description(), "DuckDuckGo") {
		t.Fatal("description should name the provider")
	}
}

func TestWebSearchBraveAndErrors(t *testing.T) {
	t.Setenv("BRAVE_TEST_KEY", "sekrit")
	var gotToken, gotCount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Subscription-Token")
		gotCount = r.URL.Query().Get("count")
		if r.URL.Query().Get("q") == "limit" {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"web":{"results":[{"title":"<strong>Rust</strong> book","url":"https://doc.rust-lang.org/book/","description":"Learn Rust"},{"title":"no url"},{"title":"Second","url":"https://example.org/2","description":""}]}}`))
	}))
	defer srv.Close()
	ws := WebSearch{Config: weburl.SearchConfig{Provider: "brave", Key: "${BRAVE_TEST_KEY}", URL: srv.URL}}
	out, err := ws.Run(context.Background(), json.RawMessage(`{"query":"rust book","max_results":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotToken != "sekrit" || gotCount != "10" {
		t.Fatalf("token %q count %q", gotToken, gotCount)
	}
	if !strings.Contains(out, "1. Rust book\n   https://doc.rust-lang.org/book/\n   Learn Rust") || !strings.Contains(out, "2. Second\n   https://example.org/2\n") || strings.Contains(out, "no url") {
		t.Fatalf("out:\n%s", out)
	}
	if _, err := ws.Run(context.Background(), json.RawMessage(`{"query":"limit"}`)); err == nil || !strings.Contains(err.Error(), "refused the search (HTTP 429)") {
		t.Fatalf("rate limit err = %v", err)
	}
	if _, err := ws.Run(context.Background(), json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("empty query accepted")
	}
}
