package anthropic

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The redirect policy drops x-api-key when a hop changes host, keeps it
// on the endpoint's own host, and keeps Go's 10-hop ceiling.
func TestRedirectPolicyStripsAPIKeyCrossHost(t *testing.T) {
	var gotKey string
	dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		w.WriteHeader(200)
	}))
	defer dst.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, dst.URL+"/steal", http.StatusFound)
	}))
	defer src.Close()

	c := newRedirectSafeClient(nil)
	hreq, _ := http.NewRequest(http.MethodGet, src.URL, nil)
	hreq.Header.Set("X-Api-Key", "sk-secret")
	resp, err := c.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotKey != "" {
		t.Fatalf("the key reached the redirect target: %q", gotKey)
	}

	// A same-host hop (a proxy or gateway redirecting among its own
	// paths) keeps the key.
	same := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			gotKey = r.Header.Get("X-Api-Key")
			w.WriteHeader(200)
			return
		}
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer same.Close()
	hreq, _ = http.NewRequest(http.MethodGet, same.URL, nil)
	hreq.Header.Set("X-Api-Key", "sk-secret")
	resp, err = c.Do(hreq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotKey != "sk-secret" {
		t.Fatalf("same-host hop lost the key: %q", gotKey)
	}
}
