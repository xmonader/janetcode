package llm

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestSameOriginRules covers the comparisons Go's own policy gets wrong
// for a provider call: a subdomain is not the same site, a scheme
// downgrade is not the same origin, and a trailing dot names the same
// host.
func TestSameOriginRules(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{"https://api.example.com/v1", "https://api.example.com/v2", true},
		{"https://API.example.com/v1", "https://api.EXAMPLE.com/v2", true},
		{"https://api.example.com/v1", "https://api.example.com./v2", true},
		{"http://api.example.com/v1", "https://api.example.com/v2", true}, // an upgrade
		// Go keeps Authorization on a hop to a subdomain of the original
		// host. For an API key that is a leak.
		{"https://api.example.com/v1", "https://evil.api.example.com/x", false},
		{"https://api.example.com/v1", "https://example.com/x", false},
		{"https://api.example.com/v1", "https://evil.com/x", false},
		// Go never compares the scheme, so it would follow this and send
		// the key and the conversation in plaintext.
		{"https://api.example.com/v1", "http://api.example.com/v1", false},
		{"https://api.example.com/v1", "https://api.example.com:8443/v1", false},
	} {
		from, err := url.Parse(tc.from)
		if err != nil {
			t.Fatal(err)
		}
		to, err := url.Parse(tc.to)
		if err != nil {
			t.Fatal(err)
		}
		if got := sameOrigin(from, to); got != tc.want {
			t.Errorf("sameOrigin(%s, %s) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// TestSafeRedirectClientRefusesAnotherOrigin: stripping the key leaves
// the request body — the system prompt, file contents, tool output —
// still going to whatever the Location names, because 307 and 308
// preserve it. The hop is refused instead.
func TestSafeRedirectClientRefusesAnotherOrigin(t *testing.T) {
	var reached bool
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer src.Close()

	c := SafeRedirectClient(nil)
	hreq, _ := http.NewRequest(http.MethodPost, src.URL, strings.NewReader(`{"messages":"the whole conversation"}`))
	hreq.Header.Set("X-Api-Key", "sk-secret")
	hreq.Header.Set("Authorization", "Bearer sk-secret")
	resp, err := c.Do(hreq)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the cross-origin redirect was followed")
	}
	if !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("err = %v", err)
	}
	if reached {
		t.Fatal("the other origin received the request")
	}
}

// TestSafeRedirectClientFollowsItsOwnHost: a gateway redirecting among
// its own paths is ordinary, and must keep working with the key.
func TestSafeRedirectClientFollowsItsOwnHost(t *testing.T) {
	var gotKey string
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			gotKey = r.Header.Get("X-Api-Key")
			w.WriteHeader(200)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer same.Close()
	c := SafeRedirectClient(nil)
	hreq, _ := http.NewRequest(http.MethodGet, same.URL, nil)
	hreq.Header.Set("X-Api-Key", "sk-secret")
	resp, err := c.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotKey != "sk-secret" {
		t.Fatalf("a same-host hop lost the key: %q", gotKey)
	}
}

// TestSafeRedirectClientKeepsACallersPolicy: a caller that set its own
// CheckRedirect keeps it — but it runs after this one, so a client handed
// in through configuration cannot switch the protection off.
func TestSafeRedirectClientKeepsACallersPolicy(t *testing.T) {
	var innerCalls int
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		innerCalls++
		return http.ErrUseLastResponse
	}}
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer same.Close()
	resp, err := SafeRedirectClient(base).Get(same.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if innerCalls != 1 {
		t.Fatalf("the caller's policy ran %d times", innerCalls)
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the caller's policy to have stopped the hop", resp.StatusCode)
	}
}
