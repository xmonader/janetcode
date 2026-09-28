package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// ErrAuthRequired is returned when a remote server answers 401 and no
// usable token is stored: the user has to run the authorization flow.
var ErrAuthRequired = errors.New("authorization required")

// AuthError is the 401 a remote server answered with. It unwraps to
// ErrAuthRequired and carries the resource metadata URL when the server
// named one.
type AuthError struct {
	Server           string
	ResourceMetadata string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("authorization required: the server answered 401; run /mcp auth %s (or set an Authorization header in its config)", e.Server)
}

func (e *AuthError) Is(target error) bool { return target == ErrAuthRequired }

// Token is what the authorization server issued for one MCP server.
type Token struct {
	ClientID      string    `json:"client_id"`
	ClientSecret  string    `json:"client_secret,omitempty"`
	AccessToken   string    `json:"access_token"`
	RefreshToken  string    `json:"refresh_token,omitempty"`
	Expiry        time.Time `json:"expiry,omitempty"`
	TokenEndpoint string    `json:"token_endpoint"`
	Scope         string    `json:"scope,omitempty"`
}

// Expired reports whether the access token should be refreshed.
func (t Token) Expired() bool {
	return !t.Expiry.IsZero() && time.Until(t.Expiry) < 30*time.Second
}

// TokenStore keeps tokens per server URL in one JSON file (mode 0600).
type TokenStore struct {
	Path string
	// mu guards the file: load and save are one read-modify-write.
	mu sync.Mutex
	// refreshMu serialises refreshes across every server in the store. An
	// authorization server usually invalidates a refresh token the moment
	// it is spent, so two parallel tool calls noticing the same expiry and
	// both spending it left one of them holding a token the server had
	// already revoked — and the loser's write could land last and store
	// it. Refreshes are rare enough that one lock for the store costs
	// nothing.
	refreshMu sync.Mutex
}

func (s *TokenStore) load() (map[string]Token, error) {
	out := map[string]Token{}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	return out, nil
}

func (s *TokenStore) save(all map[string]Token) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(s.Path, data, 0o600)
}

// Get returns the token stored for a server URL.
func (s *TokenStore) Get(serverURL string) (Token, bool) {
	if s == nil {
		return Token{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return Token{}, false
	}
	t, ok := all[normalizeURL(serverURL)]
	return t, ok && t.AccessToken != ""
}

// All returns every stored token by server URL. The doctor reads it to
// find logins that have expired with no way to renew themselves.
func (s *TokenStore) All() (map[string]Token, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Put stores a token for a server URL.
func (s *TokenStore) Put(serverURL string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	all[normalizeURL(serverURL)] = t
	return s.save(all)
}

// Delete forgets a server's token.
func (s *TokenStore) Delete(serverURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	delete(all, normalizeURL(serverURL))
	return s.save(all)
}

// Bearer returns a valid access token for the server, refreshing it first
// when it has expired. ok is false when nothing is stored.
func (s *TokenStore) Bearer(ctx context.Context, client *http.Client, serverURL string, force bool) (token string, ok bool, err error) {
	t, ok := s.Get(serverURL)
	if !ok {
		return "", false, nil
	}
	if !force && !t.Expired() {
		return t.AccessToken, true, nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	// Someone else may have refreshed while this caller waited for the
	// lock; their token is the live one, and spending the refresh token
	// again would revoke it.
	if cur, ok := s.Get(serverURL); ok && cur.AccessToken != t.AccessToken && !cur.Expired() {
		return cur.AccessToken, true, nil
	}
	if t.RefreshToken == "" {
		return "", true, fmt.Errorf("%w: the token expired and cannot be refreshed", ErrAuthRequired)
	}
	// resource names the MCP server again, as the authorization did: an
	// authorization server that scopes tokens by audience would otherwise
	// hand back one for its default audience, which this server rejects,
	// and the user would be sent back to /mcp auth after every expiry.
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}, "client_id": {t.ClientID}, "resource": {normalizeURL(serverURL)}}
	if t.ClientSecret != "" {
		form.Set("client_secret", t.ClientSecret)
	}
	nt, err := tokenRequest(ctx, client, t.TokenEndpoint, form)
	if err != nil {
		return "", true, fmt.Errorf("%w: refresh failed: %w", ErrAuthRequired, err)
	}
	nt.ClientID, nt.ClientSecret, nt.TokenEndpoint = t.ClientID, t.ClientSecret, t.TokenEndpoint
	if nt.RefreshToken == "" {
		nt.RefreshToken = t.RefreshToken
	}
	if err := s.Put(serverURL, nt); err != nil {
		return "", true, err
	}
	return nt.AccessToken, true, nil
}

func normalizeURL(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

// authMeta is the subset of OAuth server metadata (RFC 8414) we use.
type authMeta struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
}

// discover finds the authorization server for an MCP server: through the
// protected-resource metadata (RFC 9728) when published, else assuming the
// MCP server is its own authorization server, with default endpoint paths
// when no metadata is served at all.
func discover(ctx context.Context, client *http.Client, serverURL, resourceMetadata string) (authMeta, error) {
	su, err := url.Parse(serverURL)
	if err != nil || su.Host == "" {
		return authMeta{}, fmt.Errorf("bad server url %q", serverURL)
	}
	origin := su.Scheme + "://" + su.Host
	var candidates []string
	if resourceMetadata != "" {
		candidates = append(candidates, resourceMetadata)
	}
	if p := strings.TrimSuffix(su.Path, "/"); p != "" {
		candidates = append(candidates, origin+"/.well-known/oauth-protected-resource"+p)
	}
	candidates = append(candidates, origin+"/.well-known/oauth-protected-resource")
	authServer := origin
	for _, c := range candidates {
		var pr struct {
			AuthorizationServers []string `json:"authorization_servers"`
		}
		if getJSON(ctx, client, c, &pr) == nil && len(pr.AuthorizationServers) > 0 {
			authServer = strings.TrimRight(pr.AuthorizationServers[0], "/")
			break
		}
	}
	au, err := url.Parse(authServer)
	if err != nil || au.Host == "" {
		return authMeta{}, fmt.Errorf("bad authorization server %q", authServer)
	}
	aorigin := au.Scheme + "://" + au.Host
	var metaURLs []string
	if p := strings.TrimSuffix(au.Path, "/"); p != "" {
		metaURLs = append(metaURLs, aorigin+"/.well-known/oauth-authorization-server"+p, aorigin+"/.well-known/openid-configuration"+p, authServer+"/.well-known/openid-configuration")
	}
	metaURLs = append(metaURLs, aorigin+"/.well-known/oauth-authorization-server", aorigin+"/.well-known/openid-configuration")
	for _, mu := range metaURLs {
		var m authMeta
		if getJSON(ctx, client, mu, &m) == nil && m.AuthorizationEndpoint != "" && m.TokenEndpoint != "" {
			return m, nil
		}
	}
	return authMeta{
		Issuer:                authServer,
		AuthorizationEndpoint: authServer + "/authorize",
		TokenEndpoint:         authServer + "/token",
		RegistrationEndpoint:  authServer + "/register",
	}, nil
}

func getJSON(ctx context.Context, client *http.Client, target string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// AuthOptions drive Authorize.
type AuthOptions struct {
	// ServerURL is the MCP endpoint the token is for.
	ServerURL string
	// Store receives the token; required.
	Store *TokenStore
	// ClientID/ClientSecret skip dynamic registration when the server
	// wants pre-registered clients.
	ClientID, ClientSecret string
	// Scope requested; empty asks for whatever the server offers.
	Scope string
	// ResourceMetadata is the URL a 401 pointed at, when known.
	ResourceMetadata string
	// OpenURL is asked to open the browser; nil or a failure falls back to
	// telling the user through Notify.
	OpenURL func(string) error
	// Notify receives progress lines for the user.
	Notify func(string)
	// Client for the HTTP calls; nil uses a 30s-timeout client.
	Client *http.Client
	// Listen is the loopback address for the redirect; "" means 127.0.0.1:0.
	Listen string
}

// Authorize runs the OAuth 2.1 authorization-code flow with PKCE against
// the MCP server's authorization server, registering a client dynamically
// when none is configured, and stores the token. It returns when the
// browser has come back or ctx ends.
func Authorize(ctx context.Context, o AuthOptions) (Token, error) {
	if o.Store == nil {
		return Token{}, errors.New("no token store")
	}
	notify := o.Notify
	if notify == nil {
		notify = func(string) {}
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	meta, err := discover(ctx, client, o.ServerURL, o.ResourceMetadata)
	if err != nil {
		return Token{}, err
	}
	notify("authorization server: " + meta.Issuer)

	listen := o.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return Token{}, fmt.Errorf("listen for the redirect: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	clientID, clientSecret := o.ClientID, o.ClientSecret
	if clientID == "" {
		if old, ok := o.Store.Get(o.ServerURL); ok && old.ClientID != "" && old.TokenEndpoint == meta.TokenEndpoint {
			clientID, clientSecret = old.ClientID, old.ClientSecret
		}
	}
	if clientID == "" {
		if meta.RegistrationEndpoint == "" {
			return Token{}, errors.New("the server does not offer dynamic client registration; set client_id in the server config")
		}
		clientID, clientSecret, err = register(ctx, client, meta.RegistrationEndpoint, redirect, o.Scope)
		if err != nil {
			return Token{}, fmt.Errorf("register client: %w", err)
		}
		notify("registered client " + clientID)
	}

	verifier := randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomToken(16)
	scope := o.Scope
	if scope == "" {
		scope = strings.Join(meta.ScopesSupported, " ")
	}
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {redirect},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"state":                 {state},
		"resource":              {normalizeURL(o.ServerURL)},
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	authURL := meta.AuthorizationEndpoint
	if strings.Contains(authURL, "?") {
		authURL += "&" + q.Encode()
	} else {
		authURL += "?" + q.Encode()
	}

	type result struct {
		code string
		err  error
	}
	got := make(chan result, 1)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		qs := r.URL.Query()
		// The redirect lands on a loopback port anything on this machine
		// can reach, and state is the only thing that says the browser
		// coming back is the one that was sent: compare it without
		// leaking how much of it matched. A request without it is not
		// the browser, so it is turned away without ending the flow —
		// delivering it failed the authorization the user was still
		// completing, and let any local process cancel it at will.
		if subtle.ConstantTimeCompare([]byte(qs.Get("state")), []byte(state)) != 1 {
			http.Error(w, "janetcode: this redirect carried a wrong state; ignoring it", http.StatusBadRequest)
			return
		}
		var res result
		switch {
		case qs.Get("error") != "":
			res.err = fmt.Errorf("%s: %s", qs.Get("error"), qs.Get("error_description"))
		case qs.Get("code") == "":
			res.err = errors.New("redirect without a code")
		default:
			res.code = qs.Get("code")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != nil {
			fmt.Fprintf(w, "<p>janetcode: authorization failed: %s</p>", htmlEscape(res.err.Error()))
		} else {
			fmt.Fprint(w, "<p>janetcode is authorized. You can close this tab.</p>")
		}
		select {
		case got <- res:
		default:
		}
	})}
	go srv.Serve(ln)
	defer srv.Close()

	notify("open this URL to authorize janetcode:\n" + authURL)
	if o.OpenURL != nil {
		if err := o.OpenURL(authURL); err != nil {
			notify("could not open a browser: " + err.Error())
		}
	}
	var res result
	select {
	case res = <-got:
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}
	if res.err != nil {
		return Token{}, res.err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {res.code},
		"redirect_uri":  {redirect},
		"client_id":     {clientID},
		"code_verifier": {verifier},
		"resource":      {normalizeURL(o.ServerURL)},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	tok, err := tokenRequest(ctx, client, meta.TokenEndpoint, form)
	if err != nil {
		return Token{}, fmt.Errorf("exchange code: %w", err)
	}
	tok.ClientID, tok.ClientSecret, tok.TokenEndpoint = clientID, clientSecret, meta.TokenEndpoint
	if err := o.Store.Put(o.ServerURL, tok); err != nil {
		return Token{}, fmt.Errorf("save token: %w", err)
	}
	return tok, nil
}

// register performs dynamic client registration (RFC 7591) for a public
// client that authenticates with PKCE only.
func register(ctx context.Context, client *http.Client, endpoint, redirect, scope string) (id, secret string, err error) {
	body := map[string]any{
		"client_name":                "janetcode",
		"redirect_uris":              []string{redirect},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
		"application_type":           "native",
	}
	if scope != "" {
		body["scope"] = scope
	}
	data, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(data)))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.ClientID == "" {
		return "", "", fmt.Errorf("bad registration reply: %s", strings.TrimSpace(string(raw)))
	}
	return out.ClientID, out.ClientSecret, nil
}

func tokenRequest(ctx context.Context, client *http.Client, endpoint string, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Scope        string `json:"scope"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		msg := strings.TrimSpace(out.Error + " " + out.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return Token{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	t := Token{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Scope: out.Scope}
	if out.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return t, nil
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;").Replace(s)
}

// resourceMetadataHint pulls resource_metadata="..." out of a
// WWW-Authenticate header.
func resourceMetadataHint(h string) string {
	i := strings.Index(h, "resource_metadata=")
	if i < 0 {
		return ""
	}
	rest := strings.TrimPrefix(h[i+len("resource_metadata="):], "\"")
	if j := strings.IndexAny(rest, "\", "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// OpenBrowser asks the desktop to open a URL. It returns an error when no
// opener is available, so the caller can print the URL instead.
func OpenBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no graphical session")
		}
		cmd = exec.Command("xdg-open", target)
	}
	if _, err := exec.LookPath(cmd.Path); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// The opener hands the URL to the desktop and exits at once, but
	// nothing was waiting for it, so every /mcp auth left a zombie behind
	// for the rest of the session. Reap it; there is nothing to report.
	go func() { _ = cmd.Wait() }()
	return nil
}
