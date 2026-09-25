package mcpcmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/mcp"
)

const registryFixture = `{"servers":[
 {"server":{"name":"io.github.acme/pg-server","title":"Postgres","description":"Query a database.",
   "packages":[{"registryType":"npm","identifier":"@acme/pg-mcp","transport":{"type":"stdio"},
     "packageArguments":[{"type":"positional","name":"dsn","isRequired":true}],
     "environmentVariables":[{"name":"PG_PASSWORD","isSecret":true},{"name":"PG_DEBUG"}]}]}},
 {"server":{"name":"io.github.acme/py-tool","description":"A Python one.",
   "packages":[{"registryType":"pypi","identifier":"acme-mcp","transport":{"type":"stdio"}}]}},
 {"server":{"name":"ai.smithery/hosted","description":"Hosted.",
   "remotes":[{"type":"streamable-http","url":"https://server.example/mcp",
     "headers":[{"name":"Authorization","value":"Bearer {smithery_api_key}","isRequired":true,"isSecret":true}]}]}},
 {"server":{"name":"com.example/Events SSE","description":"Old transport.",
   "remotes":[{"type":"sse","url":"https://sse.example/sse","headers":[{"name":"X-Key","value":"{key}","isRequired":true}]}]}},
 {"server":{"name":"org.example/nuget-only","websiteUrl":"https://example.org",
   "packages":[{"registryType":"nuget","identifier":"Example.Mcp"}]}}
]}`

// Each registry entry becomes an /mcp add line that parses, with
// placeholders for what the user must fill in; what a line cannot carry
// is said instead of dropped.
func TestSearchBuildsAddLines(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(registryFixture))
	}))
	defer srv.Close()
	t.Setenv("JANETCODE_MCP_REGISTRY", srv.URL+"/v0.1/servers")

	hits, err := Search(context.Background(), srv.Client(), "postgres db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotQuery, "search=postgres+db") || !strings.Contains(gotQuery, "version=latest") {
		t.Errorf("query = %s", gotQuery)
	}
	want := []string{
		"/mcp add pg-server --env PG_PASSWORD=<pg_password> npx -y @acme/pg-mcp <dsn>",
		"/mcp add py-tool uvx acme-mcp",
		"/mcp add hosted --header 'Authorization: Bearer <smithery_api_key>' https://server.example/mcp",
		"/mcp add events-sse --transport sse --header X-Key=<key> https://sse.example/sse",
		"",
	}
	if len(hits) != len(want) {
		t.Fatalf("%d hits", len(hits))
	}
	for i, h := range hits {
		if h.Add != want[i] {
			t.Errorf("hit %d add = %q\n            want %q", i, h.Add, want[i])
		}
		if h.Add == "" {
			continue
		}
		// The line is one /mcp add accepts.
		args, err := mcp.SplitArgs(strings.TrimPrefix(h.Add, "/mcp add "))
		if err != nil {
			t.Fatal(err)
		}
		spec, err := mcp.ParseAdd(args)
		if err != nil || spec.Name == "" {
			t.Errorf("hit %d: /mcp add would reject %q: %v", i, h.Add, err)
		}
		if i == 2 && spec.Config.Headers["Authorization"] != "Bearer <smithery_api_key>" {
			t.Errorf("the quoted header did not survive: %+v", spec.Config.Headers)
		}
	}
	if len(hits[4].Notes) != 1 || !strings.Contains(hits[4].Notes[0], "https://example.org") {
		t.Errorf("an entry with nothing to add says nothing: %v", hits[4].Notes)
	}
	res := SearchResult("postgres db", hits)
	if !strings.Contains(res.SheetTitle, "5 servers") || !strings.Contains(res.Sheet, "Postgres  (io.github.acme/pg-server)") || len(res.Tips) != 1 {
		t.Errorf("sheet = %+v", res)
	}
}

func TestSearchFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down for maintenance", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	t.Setenv("JANETCODE_MCP_REGISTRY", srv.URL)
	if _, err := Search(context.Background(), srv.Client(), "x"); err == nil || !strings.Contains(err.Error(), "answered 503: down for maintenance") {
		t.Errorf("err = %v", err)
	}
	if _, err := Search(context.Background(), srv.Client(), "  "); err == nil || !strings.Contains(err.Error(), "usage: /mcp search") {
		t.Errorf("empty query: %v", err)
	}
	if res := SearchResult("zzz", nil); len(res.Notes) != 1 || !strings.Contains(res.Notes[0], `no server matching "zzz"`) {
		t.Errorf("no hits = %+v", res)
	}
}
