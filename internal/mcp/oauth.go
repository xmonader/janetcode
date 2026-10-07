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
	"runtime"
	"strconv"
	"strings"
	"time"
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
	// Scope is what the server's challenge said it needs, when it named
	// it (WWW-Authenticate scope="...").
	Scope string
	// StepUp is set for a 403 insufficient_scope: the login works but
	// lacks access this call needs.
	StepUp bool
}

func (e *AuthError) Error() string {
	if e.StepUp {
		need := ""
		if e.Scope != "" {
			need = " (" + e.Scope + ")"
		}
		return fmt.Sprintf("authorization required: the server needs more access than the current login grants%s; run /mcp auth %s to log in again with it, keeping the access already granted", need, e.Server)
	}
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
	// Scope is what the authorization server granted (or, when its
	// reply named none, what was asked for, RFC 6749 §5.1).
	Scope string `json:"scope,omitempty"`
	// RequiredScope is what the MCP server last said it needs and the
	// token does not carry (a 401 or a 403 insufficient_scope naming
	// scope=...). The next authorization asks for it together with
	// everything already granted, so stepping up never loses access.
	RequiredScope string `json:"required_scope,omitempty"`
}

// Expired reports whether the access token should be refreshed.
func (t Token) Expired() bool {
	return !t.Expiry.IsZero() && time.Until(t.Expiry) < 30*time.Second
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
	// IssParameter is RFC 9207's authorization_response_iss_parameter_supported:
	// the server promises to name itself in every authorization response,
	// so a response without iss cannot be its own.
	IssParameter bool `json:"authorization_response_iss_parameter_supported"`
	// fromDocument is false for the default endpoints assumed when the
	// server serves no metadata at all.
	fromDocument bool
}

// discover finds the authorization server for an MCP server: through the
// protected-resource metadata (RFC 9728) when published, else assuming the
// MCP server is its own authorization server, with default endpoint paths
// when no metadata is served at all. metadataURL, when set, is the
// authorization server's metadata document itself, for servers that
// advertise a wrong authorization server or none: discovery is skipped.
func discover(ctx context.Context, client *http.Client, serverURL, resourceMetadata, metadataURL string) (authMeta, error) {
	su, err := url.Parse(serverURL)
	if err != nil || su.Host == "" {
		return authMeta{}, fmt.Errorf("bad server url %q", serverURL)
	}
	if metadataURL != "" {
		var m authMeta
		if err := getJSON(ctx, client, metadataURL, &m); err != nil {
			return authMeta{}, fmt.Errorf("authorization server metadata %s: %w", metadataURL, err)
		}
		if m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			return authMeta{}, fmt.Errorf("authorization server metadata %s names no authorization or token endpoint", metadataURL)
		}
		m.fromDocument = true
		return m, nil
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
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
		}
		if getJSON(ctx, client, c, &pr) != nil || len(pr.AuthorizationServers) == 0 {
			continue
		}
		// RFC 9728 §3.3: the metadata must be about this server. The
		// resource_metadata URL comes from a 401 header and may name any
		// host; a document describing some other resource would point the
		// login, and the token, at an authorization server this one never
		// chose.
		if !resourceCovers(pr.Resource, serverURL) {
			continue
		}
		authServer = strings.TrimRight(pr.AuthorizationServers[0], "/")
		break
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
		if getJSON(ctx, client, mu, &m) != nil || m.AuthorizationEndpoint == "" || m.TokenEndpoint == "" {
			continue
		}
		// RFC 8414 §3.3: the issuer in the document must be the one it
		// was fetched for. A mismatch is a mix-up in the making (and the
		// iss check after the redirect compares against this value), so
		// it ends the login rather than being tried with another document.
		if m.Issuer != "" && !sameIssuer(m.Issuer, authServer) {
			return authMeta{}, fmt.Errorf("authorization server metadata at %s names issuer %q, not %q; set auth_server_metadata_url in the server's config if that is really its authorization server", mu, m.Issuer, authServer)
		}
		if m.Issuer == "" {
			m.Issuer = authServer
		}
		m.fromDocument = true
		return m, nil
	}
	return authMeta{
		Issuer:                authServer,
		AuthorizationEndpoint: authServer + "/authorize",
		TokenEndpoint:         authServer + "/token",
		RegistrationEndpoint:  authServer + "/register",
	}, nil
}

// sameIssuer compares two issuer identifiers. RFC 8414 asks for identical
// strings; a trailing slash is the one difference real servers commonly
// introduce between the identifier and its metadata, and it changes
// nothing about which server is meant.
func sameIssuer(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// resourceCovers reports whether protected-resource metadata naming
// resource describes the MCP server at serverURL: the same URL, or a
// prefix of it on the same origin (servers commonly publish their origin
// as the resource). A document that names no resource is accepted, as
// the older revision of the spec did not require it.
func resourceCovers(resource, serverURL string) bool {
	if resource == "" {
		return true
	}
	r, s := normalizeURL(resource), normalizeURL(serverURL)
	if r == s {
		return true
	}
	ru, err1 := url.Parse(r)
	su, err2 := url.Parse(s)
	if err1 != nil || err2 != nil || !strings.EqualFold(ru.Scheme, su.Scheme) || !strings.EqualFold(ru.Host, su.Host) {
		return false
	}
	return ru.Path == "" || ru.Path == "/" || strings.HasPrefix(su.Path, strings.TrimRight(ru.Path, "/")+"/")
}

// secureEndpoint refuses an OAuth endpoint that would carry a code, a
// secret or a token in clear text: OAuth 2.1 requires TLS everywhere but
// on this machine.
func secureEndpoint(what, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("bad %s %q", what, endpoint)
	}
	if u.Scheme == "https" || (u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil
	}
	return fmt.Errorf("the %s %s is not https; janetcode sends credentials only over TLS or to this machine", what, endpoint)
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// joinScopes is the union of space-separated scope lists, in first-seen
// order, without empties: "a b" + "b c" is "a b c", and "" + "" is "".
func joinScopes(lists ...string) string {
	var out []string
	seen := map[string]bool{}
	for _, l := range lists {
		for _, s := range strings.Fields(l) {
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return strings.Join(out, " ")
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
	// ServerName is the configured name of the MCP server: the token is
	// stored for this name and ServerURL together.
	ServerName string
	// ServerURL is the MCP endpoint the token is for.
	ServerURL string
	// MetadataURL, when set, is the authorization server's metadata
	// document, used instead of discovery (auth_server_metadata_url).
	MetadataURL string
	// Store receives the token; required.
	Store *TokenStore
	// ClientID/ClientSecret skip dynamic registration when the server
	// wants pre-registered clients.
	ClientID, ClientSecret string
	// Scope requested; empty asks for whatever the server offers.
	Scope string
	// ResourceMetadata is the URL a 401 pointed at, when known.
	ResourceMetadata string
	// RequiredScope is the scope a 401 or 403 named (WWW-Authenticate
	// scope="..."), when known. It is asked for along with whatever the
	// stored token already holds.
	RequiredScope string
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
	meta, err := discover(ctx, client, o.ServerURL, o.ResourceMetadata, o.MetadataURL)
	if err != nil {
		return Token{}, err
	}
	notify("authorization server: " + meta.Issuer)
	// The code, the verifier and the client secret all travel to these
	// endpoints; none of them goes anywhere in clear text.
	for _, ep := range []struct{ what, url string }{
		{"authorization endpoint", meta.AuthorizationEndpoint},
		{"token endpoint", meta.TokenEndpoint},
	} {
		if err := secureEndpoint(ep.what, ep.url); err != nil {
			return Token{}, err
		}
	}
	// PKCE is what makes a public client's code worthless to whoever
	// intercepts the redirect. A server whose metadata lists the methods
	// it supports and leaves out S256 would ignore the challenge, so the
	// login stops here rather than run without it. (A server that lists
	// none is asked anyway: older ones support S256 without saying so.)
	if len(meta.CodeChallengeMethods) > 0 && !contains(meta.CodeChallengeMethods, "S256") {
		return Token{}, fmt.Errorf("the authorization server does not support PKCE with S256 (it offers %s); janetcode will not log in without it", strings.Join(meta.CodeChallengeMethods, ", "))
	}

	listen := o.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	// The redirect carries the code; it is only ever received on this
	// machine (RFC 8252 §7.3).
	if host, _, err := net.SplitHostPort(listen); err != nil || !isLoopbackHost(host) {
		return Token{}, fmt.Errorf("the redirect listener %q must be a loopback address", listen)
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return Token{}, fmt.Errorf("listen for the redirect: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://%s/callback", ln.Addr().String())

	old, hadOld := o.Store.Get(o.ServerName, o.ServerURL)
	clientID, clientSecret := o.ClientID, o.ClientSecret
	if clientID == "" && hadOld && old.ClientID != "" && old.TokenEndpoint == meta.TokenEndpoint {
		clientID, clientSecret = old.ClientID, old.ClientSecret
	}
	// What to ask for: the configured scope, else what the server's
	// challenge named, else everything it advertises — and, on top, what
	// the current login already holds. A step-up that asked only for the
	// missing scope would come back with a token that lost the access the
	// old one had, and the next call would ask for a login again.
	base := o.Scope
	if base == "" {
		base = joinScopes(o.RequiredScope, old.RequiredScope)
	}
	if base == "" {
		base = strings.Join(meta.ScopesSupported, " ")
	}
	scope := joinScopes(base, old.Scope, old.RequiredScope, o.RequiredScope)
	if clientID == "" {
		if meta.RegistrationEndpoint == "" {
			return Token{}, errors.New("the server does not offer dynamic client registration; set client_id in the server config")
		}
		if err := secureEndpoint("registration endpoint", meta.RegistrationEndpoint); err != nil {
			return Token{}, err
		}
		clientID, clientSecret, err = register(ctx, client, meta.RegistrationEndpoint, redirect, scope)
		if err != nil {
			return Token{}, fmt.Errorf("register client: %w", err)
		}
		notify("registered client " + clientID)
	}

	verifier := randomToken(32)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	state := randomToken(16)
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
		case !issuerMatches(qs, meta):
			// RFC 9207. With more than one authorization server in play, a
			// malicious one can have the browser bring another server's
			// code here (a mix-up attack), and the exchange would hand it
			// to the wrong token endpoint. The response says which server
			// issued it; one that names another server, or names none when
			// this server promised it always would, is refused before the
			// code goes anywhere.
			res.err = fmt.Errorf("the authorization response came from issuer %q, not %q; refusing it (RFC 9207)", qs.Get("iss"), meta.Issuer)
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
	if tok.Scope == "" {
		// RFC 6749 §5.1: a reply that names no scope granted the scope
		// asked for. Recording it is what lets the next step-up keep it.
		tok.Scope = scope
	}
	if err := o.Store.Put(o.ServerName, o.ServerURL, tok); err != nil {
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
	// Decoded field by field, leniently: servers send null for optional
	// fields, "" for an unchanged scope, and expires_in as a string as
	// often as a number. A strict decode into typed fields dropped the
	// expiry silently on the string form, and a null or empty field must
	// read as absent, never as a value to send back.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	out := struct {
		AccessToken, RefreshToken, Scope, Error, ErrorDesc string
		ExpiresIn                                          int64
	}{
		AccessToken:  jsonString(fields["access_token"]),
		RefreshToken: jsonString(fields["refresh_token"]),
		Scope:        strings.TrimSpace(jsonString(fields["scope"])),
		Error:        jsonString(fields["error"]),
		ErrorDesc:    jsonString(fields["error_description"]),
		ExpiresIn:    jsonInt(fields["expires_in"]),
	}
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		msg := strings.TrimSpace(out.Error + " " + out.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		return Token{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	if tt := jsonString(fields["token_type"]); tt != "" && !strings.EqualFold(tt, "bearer") {
		return Token{}, fmt.Errorf("the token endpoint issued a %q token; MCP servers take bearer tokens", tt)
	}
	t := Token{AccessToken: out.AccessToken, RefreshToken: out.RefreshToken, Scope: out.Scope}
	if out.ExpiresIn > 0 {
		t.Expiry = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return t, nil
}

// jsonString reads a JSON string field; null, absent and non-strings are "".
func jsonString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// jsonInt reads a JSON number, or a string holding one; anything else
// (null, absent, garbage) is 0.
func jsonInt(raw json.RawMessage) int64 {
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if v, err := n.Int64(); err == nil {
			return v
		}
		if f, err := n.Float64(); err == nil {
			return int64(f)
		}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
			return v
		}
	}
	return 0
}

// issuerMatches is the RFC 9207 check on an authorization response: an
// iss parameter must name the issuer the flow started with, and a
// server that advertised authorization_response_iss_parameter_supported
// must have sent one.
func issuerMatches(qs url.Values, meta authMeta) bool {
	iss, sent := qs.Get("iss"), qs.Has("iss")
	if !sent {
		return !meta.IssParameter
	}
	// RFC 9207 §2.4 asks for a simple string comparison; the trailing
	// slash tolerance matches the metadata check (sameIssuer).
	return sameIssuer(iss, meta.Issuer)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
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
func resourceMetadataHint(h string) string { return authParam(h, "resource_metadata") }

// authParam returns one auth-param of a WWW-Authenticate header (RFC 9110
// §11.6.1): name=token or name="quoted string", with backslash escapes.
// Quoted values may hold spaces and commas — scope="files:read files:write"
// is the common case — so the value is read to its closing quote, not to
// the next separator.
func authParam(h, name string) string {
	for i := 0; i < len(h); {
		// Find the next parameter name at a word boundary.
		j := i
		for j < len(h) && (h[j] == ' ' || h[j] == ',' || h[j] == '\t') {
			j++
		}
		k := j
		for k < len(h) && h[k] != '=' && h[k] != ' ' && h[k] != ',' {
			k++
		}
		key := h[j:k]
		if k >= len(h) || h[k] != '=' {
			// A bare token (the scheme, or a token68): skip it.
			if k == j {
				k++
			}
			i = k
			continue
		}
		k++ // past '='
		var val strings.Builder
		if k < len(h) && h[k] == '"' {
			k++
			for k < len(h) && h[k] != '"' {
				if h[k] == '\\' && k+1 < len(h) {
					k++
				}
				val.WriteByte(h[k])
				k++
			}
			k++ // past the closing quote
		} else {
			for k < len(h) && h[k] != ',' && h[k] != ' ' {
				val.WriteByte(h[k])
				k++
			}
		}
		if strings.EqualFold(key, name) {
			return val.String()
		}
		i = k
	}
	return ""
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
