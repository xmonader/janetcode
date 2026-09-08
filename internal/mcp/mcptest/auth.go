// Package mcptest holds fakes for MCP tests: an HTTP MCP server that
// demands an OAuth bearer token, together with the authorization server
// that issues one (metadata, dynamic registration, authorize with an
// immediate redirect, token exchange checking PKCE, refresh).
package mcptest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// AuthServer is an MCP server that demands a bearer token, plus the
// authorization server that issues one: metadata, dynamic registration,
// authorize (redirects straight back with a code), token (checks PKCE)
// and refresh.
type AuthServer struct {
	t         *testing.T
	mu        sync.Mutex
	challenge string
	code      string
	ClientID  string
	valid     map[string]bool // access tokens accepted by /mcp
	refreshes int
	seen      []string // Authorization headers seen by /mcp
}

// Seen lists the Authorization headers /mcp received, in order.
func (a *AuthServer) Seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

// Revoke makes /mcp reject an access token.
func (a *AuthServer) Revoke(token string) {
	a.mu.Lock()
	delete(a.valid, token)
	a.mu.Unlock()
}

type rpcMessage struct {
	ID     *json.RawMessage `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func NewAuthServer(t *testing.T) (*httptest.Server, *AuthServer) {
	a := &AuthServer{t: t, valid: map[string]bool{}}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"resource":"%s/mcp","authorization_servers":["%s/auth"]}`, srv.URL, srv.URL)
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server/auth", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"issuer":"%s/auth","authorization_endpoint":"%s/auth/authorize","token_endpoint":"%s/auth/token","registration_endpoint":"%s/auth/register","scopes_supported":["mcp:tools"],"code_challenge_methods_supported":["S256"]}`, srv.URL, srv.URL, srv.URL, srv.URL)
	})
	mux.HandleFunc("/auth/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["token_endpoint_auth_method"] != "none" || body["client_name"] != "janetcode" {
			t.Errorf("registration body: %v", body)
		}
		a.mu.Lock()
		a.ClientID = "dyn-" + fmt.Sprint(len(a.valid)+1)
		id := a.ClientID
		a.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"client_id":%q,"redirect_uris":%s}`, id, mustJSON(body["redirect_uris"]))
	})
	mux.HandleFunc("/auth/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		a.mu.Lock()
		defer a.mu.Unlock()
		if q.Get("client_id") != a.ClientID || q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" || q.Get("scope") != "mcp:tools" {
			t.Errorf("authorize query: %v", q)
		}
		if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") {
			t.Errorf("redirect_uri = %q", q.Get("redirect_uri"))
		}
		a.challenge = q.Get("code_challenge")
		a.code = "code-" + randomToken(4)
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+a.code+"&state="+q.Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/auth/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != a.code || base64.RawURLEncoding.EncodeToString(sum[:]) != a.challenge || r.Form.Get("client_id") != a.ClientID {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant","error_description":"bad code or verifier"}`)
				return
			}
			a.valid["at-1"] = true
			fmt.Fprint(w, `{"access_token":"at-1","refresh_token":"rt-1","expires_in":3600,"token_type":"Bearer"}`)
		case "refresh_token":
			if r.Form.Get("refresh_token") != "rt-1" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"invalid_grant"}`)
				return
			}
			a.refreshes++
			tok := fmt.Sprintf("at-%d", a.refreshes+1)
			a.valid[tok] = true
			fmt.Fprintf(w, `{"access_token":%q,"expires_in":3600}`, tok)
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		a.mu.Lock()
		a.seen = append(a.seen, auth)
		ok := a.valid[strings.TrimPrefix(auth, "Bearer ")]
		a.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource/mcp"`, srv.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg rpcMessage
		json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{},"serverInfo":{"name":"secure","version":"1"}}}`, *msg.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"whoami","inputSchema":{"type":"object"}}]}}`, *msg.ID)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})
	srv = httptest.NewServer(mux)
	return srv, a
}
