package mcp

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/mcp/mcptest"
)

// A server that holds the exact redirect URI a client registered rejects
// that client on another loopback port, so a second login must come back
// on the port the stored registration was made for, and register again
// only when that port is taken.
func TestOAuthReusesTheRegisteredRedirectPort(t *testing.T) {
	srv, _ := mcptest.NewAuthServer(t)
	defer srv.Close()
	store := &TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	serverURL := srv.URL + "/mcp"

	login := func() (redirect string, registered bool) {
		var notes []string
		_, err := Authorize(context.Background(), AuthOptions{
			ServerName: "secure", ServerURL: serverURL, Store: store,
			Notify: func(s string) { notes = append(notes, s) },
			OpenURL: func(u string) error {
				parsed, err := url.Parse(u)
				if err != nil {
					return err
				}
				redirect = parsed.Query().Get("redirect_uri")
				go func() {
					if resp, err := http.Get(u); err == nil {
						resp.Body.Close()
					}
				}()
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return redirect, strings.Contains(strings.Join(notes, "\n"), "registered client")
	}

	first, registered := login()
	if !registered {
		t.Fatal("first login did not register a client")
	}
	second, registered := login()
	if second != first || registered {
		t.Fatalf("second login redirect = %s (registered again: %v), want %s with the stored client", second, registered, first)
	}

	// Something else holds the port: a fresh registration on a fresh one.
	u, _ := url.Parse(first)
	busy, err := net.Listen("tcp", u.Host)
	if err != nil {
		t.Skipf("cannot hold the port: %v", err)
	}
	defer busy.Close()
	third, registered := login()
	if third == first || !registered {
		t.Fatalf("third login redirect = %s (registered again: %v), want a new port and a new registration", third, registered)
	}
}
