package tool

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/weburl"
)

func TestFetchRefusesPrivateTargetsAndCrossHostRedirects(t *testing.T) {
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// A name that resolves to a private address passes a check that reads
// only the URL: "localhost." with its root dot, a subdomain of localhost,
// a wildcard-DNS name like 127.0.0.1.nip.io, a rebinding server. The
// address judged has to be the one the socket connects to.
func TestFetchRefusesANameThatResolvesToAPrivateAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("secret"))
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	// No proxy from the environment: the test is about the direct dial.
	client := &http.Client{Transport: &http.Transport{}}
	tried := 0
	for _, host := range []string{"localhost.", "janet.localhost"} {
		if _, err := net.DefaultResolver.LookupIPAddr(context.Background(), host); err != nil {
			t.Logf("%s does not resolve here: %v", host, err)
			continue
		}
		tried++
		target := "http://" + net.JoinHostPort(host, port) + "/"
		in, _ := json.Marshal(map[string]string{"url": target})
		out, err := FetchURL{Client: client}.Run(context.Background(), in)
		if err == nil || strings.Contains(out, "secret") || !strings.Contains(err.Error(), "loopback") {
			t.Errorf("%s resolves to loopback and was fetched: %q %v", target, out, err)
		}
		// The user naming the host is still what lets it through.
		typed := &weburl.Set{}
		typed.Note("look at " + target)
		if out, err := (FetchURL{Client: client, Typed: typed}).Run(context.Background(), in); err != nil || !strings.Contains(out, "secret") {
			t.Errorf("%s was named by the user and refused: %q %v", target, out, err)
		}
	}
	if tried == 0 {
		t.Skip("no localhost alias resolves on this machine")
	}
}

// A proxy the user configured on a private address is still reachable:
// the guard is for the target, and a proxy resolves the target itself.
func TestFetchThroughAPrivateProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("via proxy for " + r.URL.Host))
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	in, _ := json.Marshal(map[string]string{"url": "http://example.test/page"})
	out, err := FetchURL{Client: client}.Run(context.Background(), in)
	if err != nil || !strings.Contains(out, "via proxy for example.test") {
		t.Fatalf("the configured proxy was refused: %q %v", out, err)
	}
}

func TestFetchDialGuardJudgesTheConnectedAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80":          true,
		"[::1]:443":             true,
		"169.254.169.254:80":    true,
		"[fe80::1%eth0]:80":     true,
		"10.1.2.3:80":           true,
		"[::ffff:127.0.0.1]:80": true,
		"0.0.0.0:80":            true,
		"93.184.216.34:443":     false,
		"[2606:4700::1]:443":    false,
	} {
		if err := refusePrivateDial("tcp", addr, nil); (err != nil) != want {
			t.Errorf("%s: refused=%v, want %v (%v)", addr, err != nil, want, err)
		}
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
