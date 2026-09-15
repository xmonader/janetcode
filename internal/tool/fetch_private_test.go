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

func TestFetchRefusesPrivateTargetsAndCrossHostRedirects(t *testing.T) {
	var other *httptest.Server
	other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/away":
			http.Redirect(w, r, other.URL+"/x", http.StatusFound)
		case "/here":
			http.Redirect(w, r, "/landed", http.StatusFound)
		case "/landed":
			w.Write([]byte("arrived"))
		case "/png":
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("\x89PNG\r\n\x1a\nrest of the image"))
		}
	}))
	defer srv.Close()

	// Nothing typed: the loopback test server is refused.
	in, _ := json.Marshal(map[string]string{"url": srv.URL + "/landed"})
	if _, err := (FetchURL{}).Run(context.Background(), in); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("a private target with nothing typed: %v", err)
	}

	typed := &weburl.Set{}
	typed.Note(srv.URL + " " + other.URL)
	f := FetchURL{Typed: typed}
	run := func(path string) (string, error) {
		in, _ := json.Marshal(map[string]string{"url": srv.URL + path})
		return f.Run(context.Background(), in)
	}

	out, err := run("/away")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "redirects to") || strings.Contains(out, "secret") {
		t.Fatalf("a cross-host redirect was followed: %q", out)
	}
	if out, err := run("/here"); err != nil || !strings.Contains(out, "arrived") {
		t.Fatalf("a same-host redirect should be followed: %q %v", out, err)
	}
	out, err = run("/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "PNG image") || strings.Contains(out, "rest of the image") {
		t.Fatalf("a lying Content-Type was believed: %q", out)
	}
}

func TestFetchPreviewAndWarn(t *testing.T) {
	f := FetchURL{}
	in, _ := json.Marshal(map[string]string{"url": "https://github.com/o/r/blob/main/x.go"})
	p, err := f.Preview(context.Background(), in)
	if err != nil || !strings.Contains(p, "raw.githubusercontent.com") {
		t.Fatalf("preview = %q %v", p, err)
	}
	in, _ = json.Marshal(map[string]string{"url": "http://169.254.169.254/latest/"})
	if w := f.Warn(in); !strings.Contains(w, "link-local") {
		t.Fatalf("warn = %q", w)
	}
	in, _ = json.Marshal(map[string]string{"url": "http://example.com/"})
	if w := f.Warn(in); !strings.Contains(w, "http, not https") {
		t.Fatalf("warn = %q", w)
	}
}
