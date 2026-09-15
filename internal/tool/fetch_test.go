package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/weburl"
)

func TestFetchURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<html><head><title>T</title><style>p{}</style></head><body><script>x()</script><h1>Docs</h1><p>Hello &amp; <b>world</b></p><ul><li>one</li><li>two</li></ul></body></html>`))
		case "/data":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		case "/big":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte(strings.Repeat("a", MaxFetchBytes+100)))
		default:
			http.Error(w, "nope", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// The test server is on the loopback address, which is refused unless
	// the user named it — so say they did.
	typed := &weburl.Set{}
	typed.Note("please look at " + srv.URL)
	run := func(u string) (string, error) {
		in, _ := json.Marshal(map[string]string{"url": u})
		return FetchURL{Typed: typed}.Run(context.Background(), in)
	}
	out, err := run(srv.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "200 text/html") || strings.Contains(out, "x()") || strings.Contains(out, "<b>") {
		t.Fatalf("html not cleaned:\n%s", out)
	}
	for _, want := range []string{"Docs\n", "Hello & world", "- one\n- two"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if out, err := run(srv.URL + "/data"); err != nil || !strings.Contains(out, `{"ok":true}`) {
		t.Fatalf("json: %q %v", out, err)
	}
	if out, err := run(srv.URL + "/big"); err != nil || !strings.Contains(out, "[response truncated at 1 MB]") {
		t.Fatalf("big: %v %q", err, out[len(out)-60:])
	}
	if _, err := run(srv.URL + "/missing"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("404 err = %v", err)
	}
	for _, bad := range []string{"ftp://x/y", "/relative", "javascript:alert(1)", ""} {
		if _, err := run(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if !(FetchURL{}).Mutating() {
		t.Fatal("fetch must ask for permission")
	}
}

// A fetch cut short mid-body must fail, not hand back the bytes that did
// arrive as though they were the whole page.
func TestFetchCancelledMidBodyIsNotAPartialPage(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Content-Length", "200000")
		w.Write([]byte("the first half of the document\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	typed := &weburl.Set{}
	typed.Note("please look at " + srv.URL)
	in, _ := json.Marshal(map[string]string{"url": srv.URL + "/slow"})
	out, err := FetchURL{Typed: typed}.Run(ctx, in)
	if err == nil {
		t.Fatalf("a cancelled fetch returned a result: %q", out)
	}
	if strings.Contains(out, "first half") {
		t.Fatalf("the partial body was handed back: %q", out)
	}
}
