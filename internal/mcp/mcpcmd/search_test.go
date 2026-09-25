package mcpcmd

import (
	"context"
	"fmt"
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
 {"server":{"name":"io.github.other/py-tool","description":"A Python one.",
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

// The registry lists matches in name order and one publisher can fill a
// page with near-copies: the search shows one line per publisher, the
// best first, and skips entries the registry marked deleted.
func TestSearchRanksAndFoldsPublishers(t *testing.T) {
	entry := func(name, extra, status string) string {
		return `{"server":{"name":"` + name + `"` + extra + `},"_meta":{"io.modelcontextprotocol.registry/official":{"status":"` + status + `"}}}`
	}
	remote := `,"remotes":[{"type":"streamable-http","url":"https://spam.example/mcp"}]`
	local := `,"repository":{"url":"https://github.com/real/postgres"},"packages":[{"registryType":"npm","identifier":"pg-mcp","transport":{"type":"stdio"}}]`
	var servers []string
	for i := range 12 {
		servers = append(servers, entry(fmt.Sprintf("ai.spam/postgres-gen-%d", i), remote, "active"))
	}
	servers = append(servers,
		entry("io.github.gone/postgres", local, "deleted"),
		entry("io.github.real/postgres", local, "active"),
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"servers":[` + strings.Join(servers, ",") + `]}`))
	}))
	defer srv.Close()
	t.Setenv("JANETCODE_MCP_REGISTRY", srv.URL)

	hits, err := Search(context.Background(), srv.Client(), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Name != "io.github.real/postgres" || hits[1].More != 11 {
		for _, h := range hits {
			t.Logf("%s more=%d score=%d", h.Name, h.More, h.score)
		}
		t.Fatalf("got %d hits", len(hits))
	}
	if sheet := SearchResult("postgres", hits).Sheet; !strings.Contains(sheet, "(and 11 others from ai.spam)") {
		t.Errorf("sheet does not say what was folded:\n%s", sheet)
	}
}
