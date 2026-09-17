package anthropic

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The client New builds carries the shared redirect policy, so a
// mischievous hop — an open redirector on the endpoint's host, a proxy
// that answers 302 — cannot take the key or the conversation to another
// origin. The policy itself is covered in internal/llm.
func TestProviderClientRefusesACrossOriginRedirect(t *testing.T) {
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

	c := newRedirectSafeClient(nil)
	hreq, _ := http.NewRequest(http.MethodPost, src.URL, strings.NewReader(`{}`))
	hreq.Header.Set("X-Api-Key", "sk-secret")
	resp, err := c.Do(hreq)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the cross-origin redirect was followed")
	}
	if reached {
		t.Fatal("the other origin received the request")
	}
}
