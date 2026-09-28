package mcp

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseAdd(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want AddSpec
	}{
		{
			name: "command with arguments",
			args: []string{"docs", "npx", "-y", "docs-mcp"},
			want: AddSpec{Name: "docs", Config: ServerConfig{Command: "npx", Args: []string{"-y", "docs-mcp"}}},
		},
		{
			name: "url",
			args: []string{"remote", "https://example.test/mcp/"},
			want: AddSpec{Name: "remote", Config: ServerConfig{URL: "https://example.test/mcp"}},
		},
		{
			name: "options come before the command",
			args: []string{"docs", "--env", "TOKEN=abc", "-e", "HOME=/tmp", "server"},
			want: AddSpec{Name: "docs", Config: ServerConfig{Command: "server", Env: map[string]string{"TOKEN": "abc", "HOME": "/tmp"}}},
		},
		{
			name: "headers",
			args: []string{"remote", "https://example.test/mcp", "--header", "Authorization: Bearer t"},
			want: AddSpec{Name: "remote", Config: ServerConfig{URL: "https://example.test/mcp", Headers: map[string]string{"Authorization": "Bearer t"}}},
		},
		{
			name: "transport pins the type",
			args: []string{"remote", "https://example.test/mcp", "--transport=sse"},
			want: AddSpec{Name: "remote", Config: ServerConfig{URL: "https://example.test/mcp", Type: "sse"}},
		},
		{
			name: "a flag after the command belongs to the command",
			args: []string{"docs", "server", "--env", "x"},
			want: AddSpec{Name: "docs", Config: ServerConfig{Command: "server", Args: []string{"--env", "x"}}},
		},
		{
			name: "double dash hands the rest to the command",
			args: []string{"docs", "--", "server", "--env", "x"},
			want: AddSpec{Name: "docs", Config: ServerConfig{Command: "server", Args: []string{"--env", "x"}}},
		},
		{
			name: "from a repository",
			args: []string{"--from", "https://example.test/repo.git", "--yes"},
			want: AddSpec{From: "https://example.test/repo.git", Yes: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseAdd(tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestParseAddErrors(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{name: "nothing", args: nil, want: "usage"},
		{name: "name only", args: []string{"docs"}, want: "needs the command"},
		{name: "bad name", args: []string{"two words", "server"}, want: "spaces"},
		{name: "env on a url", args: []string{"r", "https://x.test/mcp", "-e", "A=b"}, want: "--header"},
		{name: "header on a command", args: []string{"r", "-H", "A: b", "server"}, want: "--env"},
		{name: "url with arguments", args: []string{"r", "https://x.test/mcp", "extra"}, want: "no arguments"},
		{name: "unknown option", args: []string{"--wat"}, want: "unknown option"},
		{name: "bad transport", args: []string{"r", "-t", "carrier-pigeon", "server"}, want: "stdio, http or sse"},
		{name: "transport disagrees", args: []string{"r", "-t", "http", "server"}, want: "does not match"},
		{name: "env without a value", args: []string{"r", "--env"}, want: "needs"},
		{name: "env not a pair", args: []string{"r", "--env", "TOKEN", "server"}, want: "KEY=value"},
		{name: "from with a command", args: []string{"--from", "repo", "docs", "server"}, want: "drop the command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAdd(tc.args)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	got := Describe(ServerConfig{Command: "npx", Args: []string{"-y", "s"}, Env: map[string]string{"A": "1"}})
	if got != "npx -y s · 1 env var" {
		t.Fatalf("describe: %q", got)
	}
	if got := Describe(ServerConfig{URL: "https://x.test/mcp", Headers: map[string]string{"A": "1", "B": "2"}}); got != "https://x.test/mcp · 2 headers" {
		t.Fatalf("describe: %q", got)
	}
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`add remote --header 'Authorization: Bearer x y' https://h/mcp`, []string{"add", "remote", "--header", "Authorization: Bearer x y", "https://h/mcp"}},
		{`add a --env "K=v w" -- npx -y pkg`, []string{"add", "a", "--env", "K=v w", "--", "npx", "-y", "pkg"}},
		{`a\ b 'it''s' ""`, []string{"a b", "its", ""}},
		{"  spaced   out  ", []string{"spaced", "out"}},
	}
	for _, c := range cases {
		got, err := SplitArgs(c.in)
		if err != nil || strings.Join(got, "|") != strings.Join(c.want, "|") || len(got) != len(c.want) {
			t.Errorf("SplitArgs(%s) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	if _, err := SplitArgs(`add x --header 'open`); err == nil {
		t.Error("an unclosed quote was accepted")
	}
}

func TestParsePairHeaderValueWithEquals(t *testing.T) {
	for _, tc := range []struct{ in, k, v string }{
		{"Authorization: Basic dXNlcjpwYXNz==", "Authorization", "Basic dXNlcjpwYXNz=="},
		{"X-Key: a=b", "X-Key", "a=b"},
		{"URL=http://example.test:8080/x", "URL", "http://example.test:8080/x"},
		{"TOKEN=abc", "TOKEN", "abc"},
		{"Authorization: Bearer t", "Authorization", "Bearer t"},
	} {
		k, v, err := ParsePair(tc.in)
		if err != nil || k != tc.k || v != tc.v {
			t.Errorf("ParsePair(%q) = %q, %q, %v; want %q, %q", tc.in, k, v, err, tc.k, tc.v)
		}
	}
}
