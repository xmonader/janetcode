package tool

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestNormalizeGitHubBlob(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://github.com/o/r/blob/main/a/b.go", "https://raw.githubusercontent.com/o/r/main/a/b.go"},
		{"https://github.com/o/r/raw/v1.2/x.txt", "https://raw.githubusercontent.com/o/r/v1.2/x.txt"},
		{"https://github.com/o/r", "https://github.com/o/r"},
		{"https://github.com/o/r/issues/3", "https://github.com/o/r/issues/3"},
		{"https://example.com/o/r/blob/main/x", "https://example.com/o/r/blob/main/x"},
	}
	for _, c := range cases {
		got, changed := NormalizeURL(c.in)
		if got != c.want || changed != (c.in != c.want) {
			t.Errorf("NormalizeURL(%q) = %q %v, want %q", c.in, got, changed, c.want)
		}
	}
}

func TestPrivateTarget(t *testing.T) {
	private := []string{
		"http://127.0.0.1:8080/x", "http://localhost/x", "http://[::1]/x",
		"http://169.254.169.254/latest/meta-data/", "http://10.0.0.5/x",
		"http://192.168.1.1/x", "http://172.16.0.1/x", "http://box.local/x",
		"http://metadata.google.internal/x",
	}
	for _, raw := range private {
		u, _ := url.Parse(raw)
		if PrivateTarget(u) == "" {
			t.Errorf("%s not recognised as private", raw)
		}
	}
	for _, raw := range []string{"https://example.com/x", "https://8.8.8.8/x"} {
		u, _ := url.Parse(raw)
		if why := PrivateTarget(u); why != "" {
			t.Errorf("%s judged private: %s", raw, why)
		}
	}
}

func TestURLSetRemembersWhatTheUserNamed(t *testing.T) {
	s := &URLSet{}
	s.Note("check http://localhost:3000/health and https://example.com/docs please")
	if !s.Has("localhost:3000") || !s.Has("localhost") {
		t.Fatal("the host the user named is not remembered")
	}
	if s.Has("169.254.169.254") {
		t.Fatal("a host nobody named is remembered")
	}
	if !s.Has("example.com") {
		t.Fatal("example.com missing")
	}
}

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

	typed := &URLSet{}
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
