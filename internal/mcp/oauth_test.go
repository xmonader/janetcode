package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp/mcptest"
)

func TestOAuthAuthorizeAndBearer(t *testing.T) {
	srv, a := mcptest.NewAuthServer(t)
	defer srv.Close()
	store := &TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	serverURL := srv.URL + "/mcp"
	opts := Options{HandshakeTimeout: 5 * time.Second, Tokens: store}

	// Without a token the server answers 401 and the error says what to do.
	_, err := Start(context.Background(), "secure", ServerConfig{URL: serverURL}, opts)
	var ae *AuthError
	if !errors.As(err, &ae) || !errors.Is(err, ErrAuthRequired) || !strings.Contains(err.Error(), "/mcp auth secure") {
		t.Fatalf("err = %v", err)
	}
	if ae.ResourceMetadata != srv.URL+"/.well-known/oauth-protected-resource/mcp" {
		t.Fatalf("resource metadata = %q", ae.ResourceMetadata)
	}

	// The flow: "open the browser" by following the authorize redirect.
	var notes []string
	tok, err := Authorize(context.Background(), AuthOptions{
		ServerURL: serverURL, Store: store, ResourceMetadata: ae.ResourceMetadata,
		Notify: func(s string) { notes = append(notes, s) },
		OpenURL: func(u string) error {
			go func() {
				resp, err := http.Get(u) // follows the 302 to the loopback callback
				if err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" || tok.ClientID != a.ClientID || tok.Expiry.IsZero() {
		t.Fatalf("token = %+v", tok)
	}
	if got, ok := store.Get(serverURL + "/"); !ok || got.AccessToken != "at-1" {
		t.Fatalf("store lookup = %+v %v", got, ok)
	}
	if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "registered client dyn-1") || !strings.Contains(joined, "/auth/authorize?") {
		t.Fatalf("notes:\n%s", joined)
	}

	// The token is sent; the server is usable.
	c, err := Start(context.Background(), "secure", ServerConfig{URL: serverURL}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Tools(); len(got) != 1 || got[0].Name != "whoami" {
		t.Fatalf("tools = %+v", got)
	}
	c.Close()

	// The server revokes the token: one refresh, one retry, no user prompt.
	a.Revoke("at-1")
	c, err = Start(context.Background(), "secure", ServerConfig{URL: serverURL}, opts)
	if err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	c.Close()
	if got, _ := store.Get(serverURL); got.AccessToken != "at-2" || got.RefreshToken != "rt-1" {
		t.Fatalf("refreshed token = %+v", got)
	}
	seen := strings.Join(a.Seen(), ",")
	if !strings.Contains(seen, "Bearer at-1,Bearer at-2") {
		t.Fatalf("authorization headers seen: %s", seen)
	}

	// An expired token is refreshed before the request, not after a 401.
	exp, _ := store.Get(serverURL)
	exp.Expiry = time.Now().Add(-time.Minute)
	store.Put(serverURL, exp)
	n := len(a.Seen())
	c, err = Start(context.Background(), "secure", ServerConfig{URL: serverURL}, opts)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	first := a.Seen()[n]
	if first != "Bearer at-3" {
		t.Fatalf("first header after expiry = %q", first)
	}

	// A configured Authorization header wins over the store.
	_, err = Start(context.Background(), "secure", ServerConfig{URL: serverURL, Headers: map[string]string{"Authorization": "Bearer stale"}}, opts)
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("explicit header err = %v", err)
	}

	// Rejected consent surfaces the server's error.
	_, err = Authorize(context.Background(), AuthOptions{ServerURL: serverURL, Store: store, OpenURL: func(u string) error {
		go func() {
			// Pretend the user declined: hit the callback with an error.
			cb := u[strings.Index(u, "redirect_uri=")+len("redirect_uri="):]
			cb = cb[:strings.IndexByte(cb, '&')]
			cb, _ = urlUnescape(cb)
			state := u[strings.Index(u, "state=")+len("state="):]
			if i := strings.IndexByte(state, '&'); i >= 0 {
				state = state[:i]
			}
			resp, err := http.Get(cb + "?error=access_denied&error_description=nope&state=" + state)
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "access_denied: nope") {
		t.Fatalf("declined err = %v", err)
	}
}

func urlUnescape(s string) (string, error) { return url.QueryUnescape(s) }

// The redirect listener is a loopback port anything on the machine can
// reach. A request with the wrong state is not the browser coming back,
// so it must not end the flow the user is still completing.
func TestOAuthWrongStateDoesNotAbortTheFlow(t *testing.T) {
	srv, _ := mcptest.NewAuthServer(t)
	defer srv.Close()
	store := &TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := Authorize(ctx, AuthOptions{
		ServerURL: srv.URL + "/mcp", Store: store,
		OpenURL: func(u string) error {
			pu, err := url.Parse(u)
			if err != nil {
				return err
			}
			cb := pu.Query().Get("redirect_uri")
			go func() {
				if resp, err := http.Get(cb + "?code=forged&state=wrong"); err == nil {
					resp.Body.Close()
				}
				if resp, err := http.Get(u); err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("a stray callback ended the flow: %v", err)
	}
	if tok.AccessToken == "" {
		t.Fatalf("token = %+v", tok)
	}
}

func TestOAuthDiscoveryFallsBackToDefaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	m, err := discover(context.Background(), srv.Client(), srv.URL+"/mcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if m.AuthorizationEndpoint != srv.URL+"/authorize" || m.TokenEndpoint != srv.URL+"/token" || m.RegistrationEndpoint != srv.URL+"/register" {
		t.Fatalf("meta = %+v", m)
	}
	if got := resourceMetadataHint(`Bearer realm="x", resource_metadata="https://h/.well-known/oauth-protected-resource", scope="a"`); got != "https://h/.well-known/oauth-protected-resource" {
		t.Fatalf("hint = %q", got)
	}
}
