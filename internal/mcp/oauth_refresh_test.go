package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// An authorization server usually revokes a refresh token the moment it is
// spent. Parallel tool calls all notice the same expiry at once, so an
// unguarded refresh spent it several times over and left the store holding
// whichever answer landed last — a token the server had already revoked.
func TestConcurrentRefreshSpendsTheRefreshTokenOnce(t *testing.T) {
	var (
		mu       sync.Mutex
		spent    = map[string]bool{}
		issued   atomic.Int64
		reuse    atomic.Int64
		endpoint string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		rt := r.Form.Get("refresh_token")
		mu.Lock()
		used := spent[rt]
		spent[rt] = true
		mu.Unlock()
		if used {
			reuse.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		n := issued.Add(1)
		// Slow enough that every caller piles up behind the first.
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"access-` + strconv.FormatInt(n, 10) +
			`","refresh_token":"refresh-` + strconv.FormatInt(n, 10) + `","expires_in":3600}`))
	}))
	defer srv.Close()
	endpoint = srv.URL + "/token"

	store := &TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	serverURL := "https://example.test/mcp"
	if err := store.Put(serverURL, Token{
		ClientID: "cid", AccessToken: "stale", RefreshToken: "refresh-0",
		TokenEndpoint: endpoint, Expiry: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	const callers = 8
	var wg sync.WaitGroup
	tokens := make([]string, callers)
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tokens[i], _, errs[i] = store.Bearer(context.Background(), srv.Client(), serverURL, false)
		}()
	}
	wg.Wait()

	if n := reuse.Load(); n != 0 {
		t.Fatalf("the refresh token was spent again %d times", n)
	}
	if n := issued.Load(); n != 1 {
		t.Fatalf("refreshed %d times, want once", n)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if tokens[i] != "access-1" {
			t.Fatalf("caller %d got %q, want the refreshed token", i, tokens[i])
		}
	}
	if got, _ := store.Get(serverURL); got.AccessToken != "access-1" || got.RefreshToken != "refresh-1" {
		t.Fatalf("stored %+v, want the refreshed pair", got)
	}
}
