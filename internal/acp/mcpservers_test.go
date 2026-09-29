package acp

import (
	"errors"
	"testing"
)

func TestClientServersReadsTheACPShapes(t *testing.T) {
	got, err := clientServers([]mcpServerSpec{
		{Name: "fs", Command: "npx", Args: []string{"-y", "srv"}, Env: []nameValue{{"TOKEN", "x"}}},
		{Type: "http", Name: "web", URL: "https://mcp.example/mcp", Headers: []nameValue{{"Authorization", "Bearer y"}}},
		{Type: "sse", Name: "old", URL: "https://mcp.example/sse"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if fs := got["fs"]; fs.Command != "npx" || len(fs.Args) != 2 || fs.Env["TOKEN"] != "x" || fs.URL != "" {
		t.Fatalf("stdio = %+v", fs)
	}
	if web := got["web"]; web.URL == "" || web.Type != "http" || web.Headers["Authorization"] != "Bearer y" {
		t.Fatalf("http = %+v", web)
	}
	if got["old"].Type != "sse" {
		t.Fatalf("sse = %+v", got["old"])
	}
	for _, bad := range [][]mcpServerSpec{
		{{Command: "x"}},
		{{Name: "a"}},
		{{Name: "a", Type: "http"}},
		{{Name: "a", Type: "carrier-pigeon", Command: "x"}},
	} {
		if _, err := clientServers(bad); !errors.Is(err, errBadParams) {
			t.Errorf("%+v: err = %v, want bad params", bad, err)
		}
	}
}
