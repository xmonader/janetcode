package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveTokenPath(t *testing.T) {
	t.Setenv("MCP_TF_DIR", "/run/creds")
	home, _ := os.UserHomeDir()
	for _, c := range []struct{ in, want, err string }{
		{"/etc/tok", "/etc/tok", ""},
		{"${MCP_TF_DIR}/tok", "/run/creds/tok", ""},
		{"~/tok", filepath.Join(home, "tok"), ""},
		{"rel/tok", "", "absolute"},
		{"", "", "absolute"},
	} {
		got, err := resolveTokenPath(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: err %v, want %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %q, %v want %q", c.in, got, err, c.want)
		}
	}
}

func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if tok, err := readTokenFile(write("ok", " abc.DEF-123\n")); err != nil || tok != "abc.DEF-123" {
		t.Errorf("good token: %q %v", tok, err)
	}
	for name, body := range map[string]string{
		"empty": "  \n",
		"big":   strings.Repeat("a", maxTokenFileBytes+1),
		"multi": "line one\nline two",
		"key":   "-----BEGIN KEY-----\nsecretsecret\n-----END KEY-----",
	} {
		tok, err := readTokenFile(write(name, body))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if tok != "" || strings.Contains(err.Error(), "secretsecret") {
			t.Errorf("%s: error leaks contents: %v", name, err)
		}
	}
	if _, err := readTokenFile(filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing file error should name the path: %v", err)
	}
}

// A rotated token takes effect on the next request with no reconnect, and
// the file wins over an Authorization header.
func TestHTTPTokenFileRereadPerRequest(t *testing.T) {
	srv, seen := streamableServer(t)
	defer srv.Close()
	p := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(p, []byte("one\n"), 0o600)
	c, err := Start(context.Background(), "remote", ServerConfig{URL: srv.URL, TokenFile: p, Headers: map[string]string{"Authorization": "Bearer stale"}}, Options{HandshakeTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if v, _ := seen.Load("auth"); v != "Bearer one" {
		t.Fatalf("first auth %v", v)
	}
	os.WriteFile(p, []byte("two\n"), 0o600)
	if _, err := c.CallTool(context.Background(), "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if v, _ := seen.Load("auth"); v != "Bearer two" {
		t.Fatalf("rotated token not used: %v", v)
	}
}

func TestHTTPTokenFileBadPathFailsServer(t *testing.T) {
	_, err := Start(context.Background(), "remote", ServerConfig{URL: "http://127.0.0.1:1", TokenFile: "rel/tok"}, Options{HandshakeTimeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("err %v", err)
	}
}
