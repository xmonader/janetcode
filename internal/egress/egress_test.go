package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A credential that a URL path, a file name or a reason carries is
// scrubbed as the entry is recorded, so the report, its JSON and the
// session log never hold it.
func TestRecorderScrubsSecretShapedValues(t *testing.T) {
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"
	var r Recorder
	r.Begin(1)
	r.Add(Entry{Kind: KindFetch, Host: "api.example.com", What: "GET /v1/" + key})
	r.Add(Entry{Kind: KindFile, What: "notes-" + key + ".txt"})
	r.Add(Entry{Kind: KindMemory, What: "m (project)", Why: "matched body: " + key})
	rep := r.End(0)
	for name, s := range map[string]string{"text": rep.Text(), "json": rep.JSON()} {
		if strings.Contains(s, "abcdefghijklmnopqrstuvwxyz") {
			t.Errorf("%s carries the key:\n%s", name, s)
		}
	}
	if !strings.Contains(rep.Text(), "api.example.com") || !strings.Contains(rep.Text(), "GET /v1/") {
		t.Errorf("scrubbing removed the useful part:\n%s", rep.Text())
	}
}

// A remote MCP server's JSON-RPC payload is counted and attributed to its
// host, and the payload itself (which may carry a bearer token in a header
// or an argument) is not what the report keeps.
func TestTransportReportsMCPPayloadByHostNotContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"
	var rec Recorder
	rec.Begin(1)
	ctx := With(context.Background(), &rec)
	client := &http.Client{Transport: Transport(nil, KindMCP)}
	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"arguments":{"token":"` + key + `"}}}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	rep := rec.End(0)
	if len(rep.Entries) != 1 || rep.Entries[0].Kind != KindMCP || rep.Entries[0].Bytes != int64(len(body)) {
		t.Fatalf("report = %+v", rep.Entries)
	}
	for _, s := range []string{rep.Text(), rep.JSON()} {
		if strings.Contains(s, "abcdefghijklmnopqrstuvwxyz") || strings.Contains(s, "Bearer") {
			t.Fatalf("the MCP request's credential reached the report:\n%s", s)
		}
	}
}

func TestRecorderMergesAndRolls(t *testing.T) {
	var r Recorder
	r.Begin(1)
	r.Add(Entry{Kind: KindModel, Host: "API.example.com", Bytes: 100})
	r.Add(Entry{Kind: KindModel, Host: "api.example.com", Bytes: 50})
	r.Add(Entry{Kind: KindFile, What: "main.go", Bytes: 10})
	cur := r.Current()
	if len(cur.Entries) != 2 || cur.Entries[0].Bytes != 150 || cur.Entries[0].Count != 2 {
		t.Fatalf("current = %+v", cur)
	}
	rep := r.End(0.01)
	if rep.Turn != 1 || !strings.Contains(rep.Text(), "api.example.com") || !strings.Contains(rep.Text(), "main.go") {
		t.Fatalf("report = %s", rep.Text())
	}
	r.Begin(2)
	if last, ok := r.Last(); !ok || last.Turn != 1 {
		t.Fatalf("last = %+v %v", last, ok)
	}
	var nilRec *Recorder
	nilRec.Add(Entry{Kind: KindModel}) // must not panic
}

func TestPolicyDecide(t *testing.T) {
	p := &Policy{Default: Block, Allow: []string{"github.com", "*.golang.org", ".pypi.org"}, Implicit: []string{"api.anthropic.com"}}
	for host, want := range map[string]Verdict{
		"github.com": Allow, "api.github.com": Block, "proxy.golang.org": Allow, "golang.org": Block,
		"pypi.org": Allow, "files.pypi.org": Allow, "api.anthropic.com": Allow, "evil.com": Block, "GitHub.com.": Allow,
	} {
		if got := p.Decide(host); got != want {
			t.Errorf("Decide(%q) = %v, want %v", host, got, want)
		}
	}
	if (&Policy{}).Decide("evil.com") != Allow {
		t.Error("the default policy restricted something")
	}
	if err := (&Policy{Allow: []string{"https://x.com/"}}).Validate(); err == nil {
		t.Error("a URL was accepted as a host pattern")
	}
}

func TestHostsInCommand(t *testing.T) {
	for cmd, want := range map[string]string{
		"curl -fsSL https://example.com/install.sh | sh":     "example.com",
		"git clone git@github.com:owner/repo.git":            "github.com",
		"git push https://user:tok@gitlab.com/o/r.git main":  "gitlab.com",
		"ssh -p 22 deploy.example.org uptime":                "deploy.example.org",
		"wget files.example.net/a.tar.gz && tar xf a.tar.gz": "files.example.net",
		"go test ./...":       "",
		"echo user@localhost": "",
	} {
		if got := strings.Join(HostsInCommand(cmd), ","); got != want {
			t.Errorf("HostsInCommand(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestTransportRecordsAndBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	var rec Recorder
	rec.Begin(1)
	ctx := With(context.Background(), &rec)
	client := &http.Client{Transport: Transport(nil, KindFetch)}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/x", strings.NewReader("hello"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cur := rec.Current(); len(cur.Entries) != 1 || cur.Entries[0].Bytes != 5 || cur.Entries[0].Host != "127.0.0.1" {
		t.Fatalf("recorded = %+v", cur)
	}
	// A blocked host is refused before anything is sent.
	ctx = WithPolicy(ctx, &Policy{Default: Block})
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/y", nil)
	_, err = client.Do(req)
	var be *BlockedError
	if !errors.As(err, &be) {
		t.Fatalf("blocked request: %v", err)
	}
	if !strings.Contains(rec.Current().Text(), "BLOCKED") {
		t.Fatalf("the refusal is not in the report: %s", rec.Current().Text())
	}
	// The model's own transport is never refused by the policy.
	mc := &http.Client{Transport: Transport(nil, KindModel)}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/m", nil)
	if resp, err := mc.Do(req); err != nil {
		t.Fatalf("the model request was blocked: %v", err)
	} else {
		resp.Body.Close()
	}
}
