package mcp

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A remote server whose URL names a host that resolves to this machine is
// refused at the dial, the way fetch_url is: "localhost." with its root
// dot passes every check that reads only the spelling. A URL that spells
// out 127.0.0.1 is someone saying they mean this machine, and works.
func TestRemoteServerNameResolvingPrivateIsRefused(t *testing.T) {
	srv, seen := streamableServer(t)
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	disguised := "http://localhost.:" + u.Port()
	t.Setenv("MCP_TEST_TOKEN", "tok")
	opts := Options{HandshakeTimeout: 5 * time.Second}
	_, err := Start(context.Background(), "remote", ServerConfig{URL: disguised, Headers: map[string]string{"Authorization": "Bearer ${MCP_TEST_TOKEN}"}}, opts)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a name resolving to loopback was reached: %v", err)
	}
	if v, ok := seen.Load("auth"); ok {
		t.Fatalf("the header reached the private address: %v", v)
	}
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL}, opts)
	if err != nil {
		t.Fatalf("a literal loopback URL was refused: %v", err)
	}
	c.Close()
}
