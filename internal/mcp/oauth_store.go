package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// TokenStore keeps tokens per MCP server in one JSON file (mode 0600). A
// token belongs to a server name and URL together, so two configured
// servers at one URL (two accounts on one service) keep separate logins.
// Tokens stored before that, under the URL alone, move to the first
// named server that reads them.
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

// keySep separates the server name from its URL in a store key. A URL
// cannot contain a raw tab, so the split is unambiguous.
const keySep = "\t"

// StoreKey is the key a server's token is stored under. An empty name is
// the legacy form, the URL alone.
func StoreKey(name, serverURL string) string {
	u := normalizeURL(serverURL)
	if name == "" {
		return u
	}
	return name + keySep + u
}

// ParseKey splits a store key into the server name ("" for a legacy key)
// and URL.
func ParseKey(key string) (name, serverURL string) {
	if i := strings.LastIndex(key, keySep); i >= 0 {
		return key[:i], key[i+len(keySep):]
	}
	return "", key
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

// lookup returns the entry for name and URL, moving a legacy URL-only
// entry under the name first. Callers hold s.mu.
func (s *TokenStore) lookup(name, serverURL string) (Token, bool, error) {
	all, err := s.load()
	if err != nil {
		return Token{}, false, err
	}
	key := StoreKey(name, serverURL)
	if t, ok := all[key]; ok {
		return t, true, nil
	}
	legacy := StoreKey("", serverURL)
	t, ok := all[legacy]
	if !ok || name == "" {
		return Token{}, false, nil
	}
	// A login stored under the URL alone belongs to whichever server
	// reads it first. It moves, so a second server at the same URL starts
	// without it instead of silently sharing the first one's account.
	all[key] = t
	delete(all, legacy)
	if err := s.save(all); err != nil {
		return Token{}, false, err
	}
	return t, true, nil
}

// Get returns the token stored for a server.
func (s *TokenStore) Get(name, serverURL string) (Token, bool) {
	if s == nil {
		return Token{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok, err := s.lookup(name, serverURL)
	if err != nil {
		return Token{}, false
	}
	return t, ok && t.AccessToken != ""
}

// All returns every stored token by store key (see ParseKey). The doctor
// reads it to find logins that have expired with no way to renew
// themselves.
func (s *TokenStore) All() (map[string]Token, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Put stores a token for a server.
func (s *TokenStore) Put(name, serverURL string, t Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	all[StoreKey(name, serverURL)] = t
	if name != "" {
		// The server has its own entry now; a legacy one for its URL would
		// otherwise be handed to the next server configured there.
		delete(all, StoreKey("", serverURL))
	}
	return s.save(all)
}

// Delete forgets a server's token, and any legacy one for its URL that no
// server has claimed yet.
func (s *TokenStore) Delete(name, serverURL string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	delete(all, StoreKey(name, serverURL))
	delete(all, StoreKey("", serverURL))
	return s.save(all)
}

// DeleteKey forgets the entry under one store key, as All listed it.
func (s *TokenStore) DeleteKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	delete(all, key)
	return s.save(all)
}

// NoteRequiredScope records that the server asked for scope the stored
// token lacks, so the next authorization asks for it as well. It creates
// no entry: a server with no login authorizes with the scope directly.
func (s *TokenStore) NoteRequiredScope(name, serverURL, scope string) error {
	if s == nil || strings.TrimSpace(scope) == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok, err := s.lookup(name, serverURL)
	if err != nil || !ok {
		return err
	}
	t.RequiredScope = joinScopes(t.RequiredScope, scope)
	all, err := s.load()
	if err != nil {
		return err
	}
	all[StoreKey(name, serverURL)] = t
	return s.save(all)
}

// Bearer returns a valid access token for the server, refreshing it first
// when it has expired. ok is false when nothing is stored.
func (s *TokenStore) Bearer(ctx context.Context, client *http.Client, name, serverURL string, force bool) (token string, ok bool, err error) {
	t, ok := s.Get(name, serverURL)
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
	if cur, ok := s.Get(name, serverURL); ok && cur.AccessToken != t.AccessToken && !cur.Expired() {
		return cur.AccessToken, true, nil
	}
	if t.RefreshToken == "" {
		return "", true, fmt.Errorf("%w: the token expired and cannot be refreshed", ErrAuthRequired)
	}
	// The refresh token is a credential: it goes to the token endpoint
	// recorded at login only over TLS, or to this machine.
	if err := secureEndpoint("token endpoint", t.TokenEndpoint); err != nil {
		return "", true, fmt.Errorf("%w: %w", ErrAuthRequired, err)
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
		// No rotation: the refresh token just spent is still the live one.
		nt.RefreshToken = t.RefreshToken
	}
	if nt.Scope == "" {
		// A refresh reply that names no scope keeps the scope it had
		// (RFC 6749 §6: it may not widen it, and narrowing is named).
		nt.Scope = t.Scope
	}
	nt.RequiredScope = t.RequiredScope
	if err := s.Put(name, serverURL, nt); err != nil {
		return "", true, err
	}
	return nt.AccessToken, true, nil
}
