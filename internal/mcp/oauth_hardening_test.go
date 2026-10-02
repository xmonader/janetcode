package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hardAS is an authorization server plus MCP server with knobs for the
// cases the plain fake in mcptest cannot express: which issuer the
// metadata and the redirect name, which PKCE methods are advertised,
// scopes that tools demand, and raw token responses.
type hardAS struct {
	t   *testing.T
	srv *httptest.Server

	mu sync.Mutex
	// Metadata knobs.
	metaIssuer   string   // "" = the real issuer
	issSupported bool     // authorization_response_iss_parameter_supported
	methods      []string // code_challenge_methods_supported; nil = ["S256"]
	tokenURL     string   // "" = the real token endpoint
	// Redirect knob: "" omits iss, "self" sends the real issuer, else that.
	callbackIss string
	// Token knobs.
	tokenBody func(grant string, form url.Values) string // nil = default
	// Scope demanded by tools/call (403 insufficient_scope without it).
	toolScope string

	challenge  string
	code       string
	authorizes []url.Values // every /authorize query
	tokenCalls int
	granted    map[string]string // access token -> granted scope
	n          int
}

func newHardAS(t *testing.T) *hardAS {
	a := &hardAS{t: t, granted: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"resource":"%s/mcp","authorization_servers":["%s/auth"]}`, a.srv.URL, a.srv.URL)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/auth", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		writeMeta(w, a.metaFields())
	})
	// The same document at a path no discovery would find: the override.
	mux.HandleFunc("/elsewhere/as.json", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		writeMeta(w, a.metaFields())
	})
	mux.HandleFunc("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"client_id":"dyn","client_secret":null,"client_secret_expires_at":null}`)
	})
	mux.HandleFunc("/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		a.mu.Lock()
		a.authorizes = append(a.authorizes, q)
		a.challenge = q.Get("code_challenge")
		a.code = fmt.Sprintf("code-%d", len(a.authorizes))
		cb := q.Get("redirect_uri") + "?code=" + a.code + "&state=" + url.QueryEscape(q.Get("state"))
		switch a.callbackIss {
		case "":
		case "self":
			cb += "&iss=" + url.QueryEscape(a.issuer())
		default:
			cb += "&iss=" + url.QueryEscape(a.callbackIss)
		}
		a.mu.Unlock()
		http.Redirect(w, r, cb, http.StatusFound)
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		a.tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		grant := r.Form.Get("grant_type")
		if grant == "authorization_code" {
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != a.code || base64.RawURLEncoding.EncodeToString(sum[:]) != a.challenge {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
		}
		if a.tokenBody != nil {
			fmt.Fprint(w, a.tokenBody(grant, r.Form))
			return
		}
		a.n++
		tok := fmt.Sprintf("at-%d", a.n)
		// The scope asked for at /authorize is what is granted; the reply
		// names none, as many servers' do.
		scope := ""
		if len(a.authorizes) > 0 {
			scope = a.authorizes[len(a.authorizes)-1].Get("scope")
		}
		a.granted[tok] = scope
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`, tok)
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		a.mu.Lock()
		scope, ok := a.granted[auth]
		need := a.toolScope
		a.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, a.srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"hard","version":"1"}}}`, *msg.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"write","inputSchema":{"type":"object"}}]}}`, *msg.ID)
		case "tools/call":
			if need != "" && !strings.Contains(" "+scope+" ", " "+need+" ") {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer error="insufficient_scope", scope="%s", resource_metadata="%s/.well-known/oauth-protected-resource/mcp", error_description="needs %s"`, need, a.srv.URL, need))
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"written"}]}}`, *msg.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *hardAS) issuer() string { return a.srv.URL + "/auth" }

func (a *hardAS) metaFields() map[string]any {
	iss := a.metaIssuer
	if iss == "" {
		iss = a.issuer()
	}
	methods := a.methods
	if methods == nil {
		methods = []string{"S256"}
	}
	tok := a.tokenURL
	if tok == "" {
		tok = a.srv.URL + "/auth/token"
	}
	return map[string]any{
		"issuer":                           iss,
		"authorization_endpoint":           a.srv.URL + "/auth/authorize",
		"token_endpoint":                   tok,
		"registration_endpoint":            a.srv.URL + "/auth/register",
		"scopes_supported":                 []string{"s:read", "s:write"},
		"code_challenge_methods_supported": methods,
		"authorization_response_iss_parameter_supported": a.issSupported,
	}
}

func writeMeta(w http.ResponseWriter, m map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m)
}

func (a *hardAS) url() string { return a.srv.URL + "/mcp" }

func (a *hardAS) tokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokenCalls
}

// follow plays the browser: it follows the authorize redirect to the
// loopback callback.
func follow(u string) error {
	go func() {
		if resp, err := http.Get(u); err == nil {
			resp.Body.Close()
		}
	}()
	return nil
}

func hardStore(t *testing.T) *TokenStore {
	return &TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
}

func authorizeHard(t *testing.T, a *hardAS, store *TokenStore, mod func(*AuthOptions)) (Token, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	o := AuthOptions{ServerName: "hard", ServerURL: a.url(), Store: store, OpenURL: follow}
	if mod != nil {
		mod(&o)
	}
	return Authorize(ctx, o)
}

// RFC 9207: a response that names another issuer is a mix-up attack in
// progress; the code must not reach the token endpoint.
func TestOAuthRejectsResponseFromAnotherIssuer(t *testing.T) {
	a := newHardAS(t)
	a.callbackIss = "https://evil.example/auth"
	_, err := authorizeHard(t, a, hardStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "RFC 9207") || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("err = %v", err)
	}
	if n := a.tokens(); n != 0 {
		t.Fatalf("the code was exchanged %d time(s) despite the wrong issuer", n)
	}
}

func TestOAuthAcceptsResponseFromItsIssuer(t *testing.T) {
	a := newHardAS(t)
	a.callbackIss = "self"
	a.issSupported = true
	if _, err := authorizeHard(t, a, hardStore(t), nil); err != nil {
		t.Fatal(err)
	}
}

// A server that promised to always send iss and then did not: the
// response is not its own.
func TestOAuthRejectsMissingIssWhenPromised(t *testing.T) {
	a := newHardAS(t)
	a.issSupported = true
	_, err := authorizeHard(t, a, hardStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "RFC 9207") {
		t.Fatalf("err = %v", err)
	}
	if n := a.tokens(); n != 0 {
		t.Fatalf("token endpoint called %d time(s)", n)
	}
	// Without the promise, a missing iss is the old behaviour: accepted.
	b := newHardAS(t)
	if _, err := authorizeHard(t, b, hardStore(t), nil); err != nil {
		t.Fatalf("a server that never promised iss: %v", err)
	}
}

// RFC 8414 §3.3: metadata whose issuer is not the one it was fetched for
// ends the login; the override is the way to accept such a server.
func TestOAuthMetadataIssuerMustMatch(t *testing.T) {
	a := newHardAS(t)
	a.metaIssuer = "https://other.example/auth"
	_, err := authorizeHard(t, a, hardStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "other.example") || !strings.Contains(err.Error(), "auth_server_metadata_url") {
		t.Fatalf("err = %v", err)
	}
	// A trailing slash is not a different issuer.
	b := newHardAS(t)
	b.metaIssuer = b.issuer() + "/"
	if _, err := authorizeHard(t, b, hardStore(t), nil); err != nil {
		t.Fatalf("trailing slash: %v", err)
	}
}

// auth_server_metadata_url: discovery is skipped and the named document
// is used, for servers that advertise a wrong authorization server.
func TestOAuthMetadataOverride(t *testing.T) {
	a := newHardAS(t)
	// The resource metadata points at an authorization server with no
	// metadata at all; without the override the login would use guessed
	// endpoints on it and fail.
	bogus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer bogus.Close()
	prm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"resource":"%s","authorization_servers":["%s"]}`, a.url(), bogus.URL)
	}))
	defer prm.Close()
	store := hardStore(t)
	tok, err := authorizeHard(t, a, store, func(o *AuthOptions) {
		o.ResourceMetadata = prm.URL
		o.MetadataURL = a.srv.URL + "/elsewhere/as.json"
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.TokenEndpoint != a.srv.URL+"/auth/token" {
		t.Fatalf("token endpoint = %q", tok.TokenEndpoint)
	}
	// And the config shapes reach it: janetcode's field and pi's block.
	var sc ServerConfig
	json.Unmarshal([]byte(`{"url":"x","oauth":{"authServerMetadataUrl":"https://m/as.json","clientId":"cid"}}`), &sc)
	if id, _, _, mu := sc.OAuthSettings(); id != "cid" || mu != "https://m/as.json" {
		t.Fatalf("oauth block: id=%q metadata=%q", id, mu)
	}
	json.Unmarshal([]byte(`{"url":"x","auth_server_metadata_url":"https://own/as.json","oauth":{"authServerMetadataUrl":"https://m/as.json"}}`), &sc)
	if _, _, _, mu := sc.OAuthSettings(); mu != "https://own/as.json" {
		t.Fatalf("janetcode's field should win: %q", mu)
	}
}

// RFC 9728: protected-resource metadata describing some other resource
// must not choose the authorization server.
func TestOAuthIgnoresResourceMetadataForAnotherResource(t *testing.T) {
	a := newHardAS(t)
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resource":"https://elsewhere.example/mcp","authorization_servers":["https://evil.example"]}`)
	}))
	defer evil.Close()
	m, err := discover(context.Background(), http.DefaultClient, a.url(), evil.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(m.Issuer, "evil") || m.Issuer != a.issuer() {
		t.Fatalf("issuer = %q; the foreign resource metadata was followed", m.Issuer)
	}
	for _, c := range []struct {
		res, srv string
		want     bool
	}{
		{"", "https://h/mcp", true},
		{"https://h/mcp", "https://h/mcp/", true},
		{"https://h", "https://h/mcp", true},
		{"https://h/api", "https://h/api/mcp", true},
		{"https://h/ap", "https://h/api/mcp", false},
		{"https://other/mcp", "https://h/mcp", false},
		{"http://h/mcp", "https://h/mcp", false},
	} {
		if got := resourceCovers(c.res, c.srv); got != c.want {
			t.Errorf("resourceCovers(%q, %q) = %v", c.res, c.srv, got)
		}
	}
}

// Step-up: a 403 insufficient_scope is recorded, and the next login asks
// for the new scope together with what was granted, so the new token
// keeps the old access instead of trading it for the new one.
func TestOAuthStepUpKeepsGrantedScopes(t *testing.T) {
	a := newHardAS(t)
	a.toolScope = "s:write"
	store := hardStore(t)
	if _, err := authorizeHard(t, a, store, func(o *AuthOptions) { o.Scope = "s:read" }); err != nil {
		t.Fatal(err)
	}
	opts := Options{HandshakeTimeout: 5 * time.Second, Tokens: store}
	c, err := Start(context.Background(), "hard", ServerConfig{URL: a.url()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.CallTool(context.Background(), "write", json.RawMessage(`{}`))
	c.Close()
	var ae *AuthError
	if !errors.As(err, &ae) || !ae.StepUp || ae.Scope != "s:write" || !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("call err = %v (%+v)", err, ae)
	}
	if !strings.Contains(err.Error(), "/mcp auth hard") || !strings.Contains(err.Error(), "s:write") {
		t.Fatalf("message = %q", err)
	}
	if tok, _ := store.Get("hard", a.url()); tok.RequiredScope != "s:write" || tok.Scope != "s:read" {
		t.Fatalf("stored = %+v", tok)
	}
	// One 403 is one error, not a sign-in loop.
	a.mu.Lock()
	n := len(a.authorizes)
	a.mu.Unlock()
	if n != 1 {
		t.Fatalf("authorize was hit %d times; a 403 must not start a login by itself", n)
	}

	// The step-up login. Nothing is configured, so the scope to ask for
	// comes from the server's challenge — and asking for that alone is
	// the bug: the new token would lose s:read.
	if _, err := authorizeHard(t, a, store, nil); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	asked := a.authorizes[len(a.authorizes)-1].Get("scope")
	a.mu.Unlock()
	if !sameScopes(asked, "s:read s:write") {
		t.Fatalf("step-up asked for %q, want the granted scope and the new one", asked)
	}
	c, err = Start(context.Background(), "hard", ServerConfig{URL: a.url()}, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.CallTool(context.Background(), "write", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("after step-up: %v", err)
	}
	if tok, _ := store.Get("hard", a.url()); tok.RequiredScope != "" || !sameScopes(tok.Scope, "s:read s:write") {
		t.Fatalf("after step-up stored = %+v", tok)
	}
	// The access s:read gave is still there.
	a.toolScope = "s:read"
	if _, err := c.CallTool(context.Background(), "write", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("the step-up lost the earlier scope: %v", err)
	}
}

// sameScopes compares two scope lists as sets.
func sameScopes(a, b string) bool {
	fa, fb := strings.Fields(a), strings.Fields(b)
	if len(fa) != len(fb) {
		return false
	}
	set := map[string]bool{}
	for _, s := range fa {
		set[s] = true
	}
	for _, s := range fb {
		if !set[s] {
			return false
		}
	}
	return true
}

// Tokens belong to a server name and URL together: two servers at one
// URL keep separate logins, and a legacy URL-only entry moves to the
// first server that reads it.
func TestTokenStorePerServerAndMigration(t *testing.T) {
	store := hardStore(t)
	const u = "https://mcp.example/mcp"
	if err := store.Put("", u, Token{AccessToken: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if tok, ok := store.Get("work", u+"/"); !ok || tok.AccessToken != "legacy" {
		t.Fatalf("first reader: %+v %v", tok, ok)
	}
	if _, ok := store.Get("personal", u); ok {
		t.Fatal("the legacy login was handed to a second server at the same URL")
	}
	all, _ := store.All()
	if _, ok := all[u]; ok || len(all) != 1 {
		t.Fatalf("after migration: %v", all)
	}
	if err := store.Put("personal", u, Token{AccessToken: "mine"}); err != nil {
		t.Fatal(err)
	}
	if a, _ := store.Get("work", u); a.AccessToken != "legacy" {
		t.Fatalf("work = %+v", a)
	}
	if b, _ := store.Get("personal", u); b.AccessToken != "mine" {
		t.Fatalf("personal = %+v", b)
	}
	if err := store.Delete("work", u); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("work", u); ok {
		t.Fatal("work survived delete")
	}
	if _, ok := store.Get("personal", u); !ok {
		t.Fatal("deleting work removed personal")
	}
	if name, su := ParseKey(StoreKey("personal", u)); name != "personal" || su != u {
		t.Fatalf("ParseKey = %q %q", name, su)
	}
	// The file stays private.
	if fi, err := os.Stat(store.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v %v", fi.Mode(), err)
	}
}

// Empty, null and string-typed optional fields are absent values, not
// errors or values to echo back.
func TestTokenResponseToleratesOptionalFields(t *testing.T) {
	for _, c := range []struct {
		body       string
		wantExpiry bool
		wantScope  string
		wantErr    string
	}{
		{`{"access_token":"a","scope":"","expires_in":null,"refresh_token":null,"token_type":"Bearer"}`, false, "", ""},
		{`{"access_token":"a","expires_in":"3600"}`, true, "", ""},
		{`{"access_token":"a","expires_in":3600.0,"scope":"  x y "}`, true, "x y", ""},
		{`{"access_token":"a","token_type":"bearer"}`, false, "", ""},
		{`{"access_token":"a","token_type":"mac"}`, false, "", "mac"},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, c.body) }))
		tok, err := tokenRequest(context.Background(), srv.Client(), srv.URL, url.Values{})
		srv.Close()
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v", c.body, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.body, err)
			continue
		}
		if tok.Expiry.IsZero() == c.wantExpiry || tok.Scope != c.wantScope || tok.RefreshToken != "" {
			t.Errorf("%s: token = %+v", c.body, tok)
		}
	}
	// "scope": "" from the code exchange records the scope asked for, so
	// the next step-up still includes it.
	a := newHardAS(t)
	a.tokenBody = func(string, url.Values) string {
		return `{"access_token":"x","scope":"","expires_in":null,"refresh_token":null}`
	}
	tok, err := authorizeHard(t, a, hardStore(t), func(o *AuthOptions) { o.Scope = "s:read" })
	if err != nil {
		t.Fatal(err)
	}
	if tok.Scope != "s:read" {
		t.Fatalf("scope = %q", tok.Scope)
	}
}

// A refresh reply without scope keeps the scope (and the pending
// step-up) the token had; rotation replaces the refresh token.
func TestRefreshKeepsScopeAndRotates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new","refresh_token":"rt-2","expires_in":3600,"scope":null}`)
	}))
	defer srv.Close()
	store := hardStore(t)
	store.Put("s", "https://h/mcp", Token{AccessToken: "old", RefreshToken: "rt-1", Expiry: time.Now().Add(-time.Hour),
		TokenEndpoint: srv.URL, Scope: "a b", RequiredScope: "c"})
	got, ok, err := store.Bearer(context.Background(), srv.Client(), "s", "https://h/mcp", false)
	if err != nil || !ok || got != "new" {
		t.Fatalf("bearer = %q %v %v", got, ok, err)
	}
	tok, _ := store.Get("s", "https://h/mcp")
	if tok.Scope != "a b" || tok.RequiredScope != "c" || tok.RefreshToken != "rt-2" {
		t.Fatalf("stored = %+v", tok)
	}
}

// Credentials only travel over TLS or to this machine.
func TestOAuthRefusesCleartextEndpoints(t *testing.T) {
	a := newHardAS(t)
	a.tokenURL = "http://auth.example.test/token"
	_, err := authorizeHard(t, a, hardStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "not https") {
		t.Fatalf("authorize err = %v", err)
	}
	// A refresh to a stored cleartext endpoint is refused without a
	// request (the refresh token would go out in the clear).
	store := hardStore(t)
	store.Put("s", "https://h/mcp", Token{AccessToken: "old", RefreshToken: "rt", Expiry: time.Now().Add(-time.Hour), TokenEndpoint: "http://auth.example.test/token"})
	_, _, err = store.Bearer(context.Background(), http.DefaultClient, "s", "https://h/mcp", false)
	if err == nil || !strings.Contains(err.Error(), "not https") || !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("refresh err = %v", err)
	}
	for _, ep := range []string{"https://x/token", "http://127.0.0.1:9/t", "http://localhost/t", "http://[::1]:8/t"} {
		if err := secureEndpoint("e", ep); err != nil {
			t.Errorf("%s refused: %v", ep, err)
		}
	}
}

// A server that advertises PKCE methods without S256 would ignore the
// challenge; the login does not run without it.
func TestOAuthRequiresS256(t *testing.T) {
	a := newHardAS(t)
	a.methods = []string{"plain"}
	_, err := authorizeHard(t, a, hardStore(t), nil)
	if err == nil || !strings.Contains(err.Error(), "S256") {
		t.Fatalf("err = %v", err)
	}
}

func TestOAuthRedirectListenerMustBeLoopback(t *testing.T) {
	a := newHardAS(t)
	_, err := authorizeHard(t, a, hardStore(t), func(o *AuthOptions) { o.Listen = "0.0.0.0:0" })
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthParam(t *testing.T) {
	h := `Bearer realm="x, y", error="insufficient_scope", scope="files:read files:write", error_description="say \"hi\"", resource_metadata=https://h/.well-known/oauth-protected-resource`
	for name, want := range map[string]string{
		"realm":             "x, y",
		"error":             "insufficient_scope",
		"scope":             "files:read files:write",
		"error_description": `say "hi"`,
		"resource_metadata": "https://h/.well-known/oauth-protected-resource",
		"missing":           "",
	} {
		if got := authParam(h, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := joinScopes("a b", "", " b  c ", "a"); got != "a b c" {
		t.Errorf("joinScopes = %q", got)
	}
}
