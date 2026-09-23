package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGuardRefusesOtherPages: a page on another site could POST "!cmd"
// to /api/prompt and run it, and a rebound DNS name could read the
// transcript. The server's own page, and a client that is not a
// browser page, still get through.
func TestGuardRefusesOtherPages(t *testing.T) {
	h := newServer(context.Background(), Options{Addr: "127.0.0.1:8787"}).handler()
	cases := []struct {
		name, method, path, host string
		headers                  map[string]string
		want                     int
	}{
		{"cross-site POST (text/plain, no preflight)", "POST", "/api/prompt", "127.0.0.1:8787",
			map[string]string{"Origin": "https://evil.example", "Content-Type": "text/plain"}, 403},
		{"cross-site by fetch metadata", "POST", "/api/prompt", "127.0.0.1:8787",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"another localhost port is same-site, not same-origin", "POST", "/api/permission", "127.0.0.1:8787",
			map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://127.0.0.1:3000"}, 403},
		{"sandboxed frame", "POST", "/api/prompt", "127.0.0.1:8787",
			map[string]string{"Origin": "null"}, 403},
		{"websocket terminal from another page", "GET", "/api/term", "127.0.0.1:8787",
			map[string]string{"Origin": "https://evil.example"}, 403},
		{"rebound DNS name reading the transcript", "GET", "/api/state", "evil.example:8787",
			map[string]string{"Sec-Fetch-Site": "same-origin"}, 403},
		{"own page", "GET", "/api/state", "127.0.0.1:8787",
			map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8787"}, 200},
		{"own page via localhost", "GET", "/api/state", "localhost:8787",
			map[string]string{"Origin": "http://localhost:8787"}, 200},
		{"curl", "GET", "/api/state", "127.0.0.1:8787", nil, 200},
		{"a link to the page from elsewhere opens it", "GET", "/", "127.0.0.1:8787",
			map[string]string{"Sec-Fetch-Site": "cross-site"}, 200},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.path, strings.NewReader(`{"text":"!true"}`))
		r.Host = c.host
		for k, v := range c.headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.name, c.method, c.path, w.Code, c.want)
		}
	}
}

// Bound to every interface, the server is reached by names the user
// picks (a LAN address, a hostname), so only the origin check applies.
func TestGuardOnAllInterfacesChecksOnlyOrigin(t *testing.T) {
	h := newServer(context.Background(), Options{Addr: "0.0.0.0:8787"}).handler()
	r := httptest.NewRequest("GET", "/api/state", nil)
	r.Host = "devbox.lan:8787"
	r.Header.Set("Origin", "http://devbox.lan:8787")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("own page on a LAN name = %d", w.Code)
	}
}
