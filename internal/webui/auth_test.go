package webui

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Off loopback the server runs commands for anyone who can reach it, so
// every request needs the token: from the printed link (traded for a
// cookie), or as a bearer header.
func TestTokenGuardsEveryRequest(t *testing.T) {
	h := newServer(context.Background(), Options{Addr: "0.0.0.0:8787", Token: "s3cret"}).handler()
	do := func(method, path string, header map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(`{"text":"!true"}`))
		r.Host = "devbox.lan:8787"
		for k, v := range header {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/state", "/api/prompt", "/events", "/api/term", "/static/app.js"} {
		if w := do("GET", path, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s without a token = %d", path, w.Code)
		}
	}
	if w := do("GET", "/", nil); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `name="token"`) {
		t.Errorf("the page without a token should ask for it: %d", w.Code)
	}
	if w := do("GET", "/?token=wrong", nil); w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "not the one") {
		t.Errorf("a wrong token = %d", w.Code)
	}
	w := do("GET", "/?token=s3cret", nil)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("the right token should set a cookie and drop itself from the URL: %d %q", w.Code, w.Header().Get("Location"))
	}
	c := w.Result().Cookies()
	if len(c) != 1 || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie = %+v", c)
	}
	if w := do("GET", "/api/state", map[string]string{"Cookie": c[0].Name + "=" + c[0].Value}); w.Code != http.StatusOK {
		t.Errorf("with the cookie = %d", w.Code)
	}
	if w := do("GET", "/api/state", map[string]string{"Cookie": c[0].Name + "=nope"}); w.Code != http.StatusUnauthorized {
		t.Errorf("with a forged cookie = %d", w.Code)
	}
	if w := do("GET", "/api/state", map[string]string{"Authorization": "Bearer s3cret"}); w.Code != http.StatusOK {
		t.Errorf("with a bearer token = %d", w.Code)
	}
}

// Run makes a token by itself, on loopback too: other users of the
// machine can reach 127.0.0.1. The URL it reports carries it.
func TestRunAddsAToken(t *testing.T) {
	for _, c := range []struct {
		addr      string
		wantToken bool
	}{{"127.0.0.1:0", true}, {"0.0.0.0:0", true}} {
		ctx, cancel := context.WithCancel(context.Background())
		urls := make(chan string, 1)
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, Options{Addr: c.addr, Cwd: t.TempDir(), Opened: func(u string) { urls <- u }})
		}()
		var u string
		select {
		case u = <-urls:
		case err := <-done:
			t.Fatalf("%s: Run returned %v", c.addr, err)
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: no URL", c.addr)
		}
		if got := strings.Contains(u, "?token="); got != c.wantToken {
			t.Errorf("%s: url %q", c.addr, u)
		}
		if c.wantToken {
			// The printed link works from a real client: it lands on the
			// page with the cookie set.
			_, port, _ := net.SplitHostPort(strings.TrimPrefix(strings.Split(u, "/?")[0], "http://"))
			link := "http://127.0.0.1:" + port + "/?" + strings.Split(u, "/?")[1]
			jar, _ := cookiejar.New(nil)
			resp, err := (&http.Client{Jar: jar}).Get(link)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || strings.Contains(string(body), `name="token"`) {
				t.Errorf("printed link = %d", resp.StatusCode)
			}
		}
		cancel()
		<-done
	}
}
