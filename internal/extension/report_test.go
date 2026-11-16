package extension

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/skill"
)

// reply answers one JSON-RPC request of the test server, shared by the
// stdio and HTTP forms. A tools/call is recorded: the report must never
// make one.
func reply(method string, id *json.RawMessage, onCall func()) map[string]any {
	res := map[string]any{}
	switch method {
	case "initialize":
		res = map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}, "prompts": map[string]any{}, "resources": map[string]any{}},
			"serverInfo":      map[string]any{"name": "t", "version": "1"},
		}
	case "tools/list":
		ro := map[string]any{"readOnlyHint": true}
		res = map[string]any{"tools": []map[string]any{
			{"name": "lookup", "inputSchema": map[string]any{"type": "object"}, "annotations": ro},
			{"name": "search", "inputSchema": map[string]any{"type": "object"}, "annotations": ro},
			{"name": "write_file", "inputSchema": map[string]any{"type": "object"}},
		}}
	case "prompts/list":
		res = map[string]any{"prompts": []map[string]any{{"name": "review", "description": "Review"}}}
	case "resources/list":
		res = map[string]any{"resources": []map[string]any{{"uri": "file:///a", "name": "a"}, {"uri": "file:///b", "name": "b"}}}
	case "tools/call":
		onCall()
		res = map[string]any{"content": []map[string]any{{"type": "text", "text": "ran"}}}
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": res}
}

// TestMain doubles as a stdio MCP server when EXT_STDIO_SERVER names a
// file; a tools/call appends to it.
func TestMain(m *testing.M) {
	if marker := os.Getenv("EXT_STDIO_SERVER"); marker != "" {
		in := bufio.NewScanner(os.Stdin)
		in.Buffer(make([]byte, 1<<16), 1<<24)
		out := bufio.NewWriter(os.Stdout)
		for in.Scan() {
			var msg struct {
				ID     *json.RawMessage `json:"id"`
				Method string           `json:"method"`
			}
			if json.Unmarshal(in.Bytes(), &msg) != nil || msg.ID == nil {
				continue
			}
			b, _ := json.Marshal(reply(msg.Method, msg.ID, func() { _ = os.WriteFile(marker, []byte("called"), 0o600) }))
			out.Write(append(b, '\n'))
			out.Flush()
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func stdioConfig(marker string) mcp.ServerConfig {
	return mcp.ServerConfig{Command: os.Args[0], Args: []string{"--api-key=hunter2hunter2hunter2", "--token", "tok-abcdefgh12345678"},
		Env: map[string]string{"EXT_STDIO_SERVER": marker, "API_TOKEN": "supersecretvalue123456"}}
}

func probeFn(t *testing.T, started *atomic.Int32) func(context.Context, string, mcp.ServerConfig) (*mcp.Client, error) {
	return func(ctx context.Context, name string, cfg mcp.ServerConfig) (*mcp.Client, error) {
		started.Add(1)
		return mcp.Start(ctx, name, cfg, mcp.Options{Root: t.TempDir(), Version: "test", HandshakeTimeout: 20 * time.Second})
	}
}

func byName(r Report, name string) MCPRow {
	for _, m := range r.MCP {
		if m.Name == name {
			return m
		}
	}
	return MCPRow{}
}

func toolNamed(m MCPRow, name string) ToolRow {
	for _, t := range m.Tools {
		if t.Name == name {
			return t
		}
	}
	return ToolRow{}
}

func TestStdioServerIsProbedWithoutCallingATool(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	var started atomic.Int32
	st := Store{Path: filepath.Join(t.TempDir(), FileName)}
	in := Inputs{Version: "v", Cwd: "/proj", ConfigPath: "/home/u/config.json", Store: st,
		Global: map[string]mcp.ServerConfig{"local": stdioConfig(marker)},
		Probe:  probeFn(t, &started), DeferAbove: 4}
	r := Build(context.Background(), in)

	m := byName(r, "local")
	if m.State != "probed" || m.Transport != "stdio" || !m.Found || m.Scope != ScopeUser {
		t.Fatalf("row = %+v", m)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the report called a tool")
	}
	if len(m.Tools) != 3 || !toolNamed(m, "lookup").ReadOnly || toolNamed(m, "write_file").ReadOnly {
		t.Fatalf("tools = %+v", m.Tools)
	}
	// 3 tools plus list_resources and read_resource is 5, over 4.
	if !toolNamed(m, "lookup").Deferred {
		t.Fatal("tools past the defer threshold are not marked deferred")
	}
	if len(m.Prompts) != 1 || m.Prompts[0] != "review" || m.Resources == nil || *m.Resources != 2 {
		t.Fatalf("prompts %v resources %v", m.Prompts, m.Resources)
	}
	if m.Trust.Status != StatusTrusted || !strings.Contains(m.Trust.Reason, "authorship") {
		t.Fatalf("trust = %+v", m.Trust)
	}
	if m.Auth.Kind != "env" || !strings.Contains(m.Auth.Detail, "API_TOKEN") {
		t.Fatalf("auth = %+v", m.Auth)
	}

	for _, out := range []string{r.Text(), r.JSON()} {
		for _, leak := range []string{"hunter2hunter2", "tok-abcdefgh", "supersecretvalue"} {
			if strings.Contains(out, leak) {
				t.Fatalf("the report leaked %q:\n%s", leak, out)
			}
		}
	}
	if !strings.Contains(r.Text(), "--api-key=[redacted]") {
		t.Fatalf("args not shown redacted:\n%s", r.Text())
	}
}

// A server that is not allowed to connect is never started, however it
// got that way: untrusted project file, disabled, or an unreadable store.
func TestNothingIsStartedForANonTrustedServer(t *testing.T) {
	var started atomic.Int32
	dir := t.TempDir()
	st := Store{Path: filepath.Join(dir, FileName)}
	if err := st.Set(UserKey("off"), Full("x").Without(Connect)); err != nil {
		t.Fatal(err)
	}
	cfg := stdioConfig(filepath.Join(dir, "m"))
	in := Inputs{Cwd: "/proj", Store: st, Probe: probeFn(t, &started),
		Global:     map[string]mcp.ServerConfig{"off": cfg},
		Project:    map[string]mcp.ServerConfig{"proj": cfg},
		ProjectMCP: Verdict{Trusted: false, Why: "the project's trust level is untrusted"}}
	r := Build(context.Background(), in)
	if started.Load() != 0 {
		t.Fatalf("%d servers were started", started.Load())
	}
	if m := byName(r, "off"); m.Trust.Status != StatusDisabled || m.State != "not started" {
		t.Fatalf("disabled row = %+v", m)
	}
	m := byName(r, "proj")
	if m.Scope != ScopeProject || m.Trust.Status != StatusUntrusted || !strings.Contains(m.Trust.Reason, "untrusted") {
		t.Fatalf("project row = %+v", m)
	}

	// An unreadable store refuses and says so.
	if err := os.WriteFile(st.Path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Project = nil
	r = Build(context.Background(), in)
	if started.Load() != 0 {
		t.Fatal("a server was started with an unreadable grant store")
	}
	if len(r.Notes) == 0 || byName(r, "off").Trust.Status != StatusRefused {
		t.Fatalf("notes %v row %+v", r.Notes, byName(r, "off"))
	}
}

func TestRestrictedServerShowsBlockedTools(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	var started atomic.Int32
	st := Store{Path: filepath.Join(t.TempDir(), FileName)}
	g := Restricted("installed with /mcp add")
	g.DisabledTools = []string{"search"}
	if err := st.Set(ProjectKey("/proj", "new"), g); err != nil {
		t.Fatal(err)
	}
	in := Inputs{Cwd: "/proj", Store: st, Probe: probeFn(t, &started),
		Project:    map[string]mcp.ServerConfig{"new": stdioConfig(marker)},
		ProjectMCP: Verdict{Trusted: true, Why: "approved with /mcp trust"}}
	m := byName(Build(context.Background(), in), "new")
	if m.Trust.Status != StatusRestricted || m.Trust.Reason != "installed with /mcp add" {
		t.Fatalf("trust = %+v", m.Trust)
	}
	if !toolNamed(m, "lookup").Callable {
		t.Fatal("read-only tool of a restricted server is not callable")
	}
	w := toolNamed(m, "write_file")
	if w.Callable || !strings.Contains(w.Blocked, "/mcp enable new mutate") {
		t.Fatalf("mutating tool: %+v", w)
	}
	if s := toolNamed(m, "search"); s.Callable || !strings.Contains(s.Blocked, "switched off") {
		t.Fatalf("switched-off tool: %+v", s)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a tool was called")
	}
}

// A remote server (Streamable HTTP) is described and probed, its URL
// credentials hidden, and its OAuth state reported without the token.
func TestRemoteServerAndOAuthState(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     *json.RawMessage `json:"id"`
			Method string           `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		if msg.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply(msg.Method, msg.ID, func() { calls.Add(1) }))
	}))
	defer srv.Close()

	dir := t.TempDir()
	tokens := &mcp.TokenStore{Path: filepath.Join(dir, "mcp-auth.json")}
	rawURL := srv.URL + "/mcp?access_token=URLSECRET12345&mode=x"
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	data, _ := json.Marshal(map[string]mcp.Token{
		mcp.StoreKey("oauthy", srv.URL+"/oauth"): {AccessToken: "ACCESSSECRET999", RefreshToken: "REFRESHSECRET999", Expiry: now.Add(-time.Hour), Scope: "mcp:tools"},
	})
	if err := os.WriteFile(tokens.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var started atomic.Int32
	in := Inputs{Cwd: "/proj", Store: Store{Path: filepath.Join(dir, FileName)}, Tokens: tokens, Now: now,
		Probe: probeFn(t, &started),
		Global: map[string]mcp.ServerConfig{
			"remote": {URL: rawURL, Headers: map[string]string{"Authorization": "Bearer HEADERSECRET1234567890"}},
			"oauthy": {URL: srv.URL + "/oauth"},
			"bare":   {URL: srv.URL + "/bare", Type: "sse"},
			"tf":     {URL: srv.URL + "/tf", TokenFile: "/nonexistent/token"},
		}}
	r := Build(context.Background(), in)

	rem := byName(r, "remote")
	if rem.Transport != "http" || rem.State != "probed" || len(rem.Tools) != 3 {
		t.Fatalf("remote row = %+v", rem)
	}
	if strings.Contains(rem.URL, "URLSECRET") || !strings.Contains(rem.URL, "mode=x") {
		t.Fatalf("url = %q", rem.URL)
	}
	if rem.Auth.Kind != "headers" || !strings.Contains(rem.Auth.Detail, "Authorization") {
		t.Fatalf("auth = %+v", rem.Auth)
	}
	if o := byName(r, "oauthy").Auth; o.Kind != "oauth" || !strings.Contains(o.Detail, "expired 1h") || !strings.Contains(o.Detail, "refresh token is stored") {
		t.Fatalf("oauth = %+v", o)
	}
	if a := byName(r, "bare").Auth; a.Kind != "none" || !strings.Contains(a.Detail, "/mcp auth bare") {
		t.Fatalf("bare = %+v", a)
	}
	if byName(r, "bare").Transport != "sse" {
		t.Fatal("explicit transport type not reported")
	}
	if a := byName(r, "tf").Auth; a.Kind != "token_file" || !strings.Contains(a.Detail, "not readable") {
		t.Fatalf("token file = %+v", a)
	}
	if calls.Load() != 0 {
		t.Fatal("the report called a tool on a remote server")
	}
	for _, out := range []string{r.Text(), r.JSON()} {
		for _, leak := range []string{"URLSECRET", "ACCESSSECRET", "REFRESHSECRET", "HEADERSECRET"} {
			if strings.Contains(out, leak) {
				t.Fatalf("leaked %q:\n%s", leak, out)
			}
		}
	}
}

// A running session is described from what its servers already said:
// nothing is probed.
func TestLiveServerIsNotProbed(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "called")
	var started atomic.Int32
	c, err := probeFn(t, &started)(context.Background(), "live", stdioConfig(marker))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	started.Store(0)
	in := Inputs{Cwd: "/p", Store: Store{Path: filepath.Join(t.TempDir(), FileName)}, Probe: probeFn(t, &started),
		Global: map[string]mcp.ServerConfig{"live": stdioConfig(marker), "failed": stdioConfig(marker)},
		Live: func(name string) (LiveServer, bool) {
			switch name {
			case "live":
				return LiveServer{State: "running", Client: c}, true
			case "failed":
				return LiveServer{State: "failed", Err: os.ErrNotExist}, true
			}
			return LiveServer{}, false
		}}
	r := Build(context.Background(), in)
	if started.Load() != 0 {
		t.Fatal("a live session started servers to describe them")
	}
	if m := byName(r, "live"); m.State != "running" || len(m.Tools) != 3 {
		t.Fatalf("live row = %+v", m)
	}
	if m := byName(r, "failed"); m.State != "failed" || m.StateDetail == "" || len(m.Tools) != 0 {
		t.Fatalf("failed row = %+v", m)
	}
}

func TestShadowedGlobalServer(t *testing.T) {
	cfg := mcp.ServerConfig{Command: "definitely-not-on-path-xyz"}
	in := Inputs{Cwd: "/p", Store: Store{Path: filepath.Join(t.TempDir(), FileName)},
		Global:     map[string]mcp.ServerConfig{"dup": cfg},
		Project:    map[string]mcp.ServerConfig{"dup": cfg},
		ProjectMCP: Verdict{Trusted: true, Why: "ok"}}
	r := Build(context.Background(), in)
	var g, p MCPRow
	for _, m := range r.MCP {
		if m.Scope == ScopeUser {
			g = m
		} else {
			p = m
		}
	}
	if !g.Shadowed || g.State != "shadowed" || p.Shadowed {
		t.Fatalf("global %+v project %+v", g, p)
	}
	if p.Found || !strings.Contains(r.Text(), "NOT FOUND on PATH") {
		t.Fatalf("a missing executable is not reported:\n%s", r.Text())
	}
	if p.State != "not started" {
		t.Fatalf("state = %q", p.State)
	}
}

func TestHooksAndSkillsAreDescribedNotRun(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	no := false
	user := hook.Config{Hooks: hook.Hooks{"pre_tool": {{Command: "touch " + marker, Network: &no}}}}
	proj := hook.Config{Hooks: hook.Hooks{
		"pre_tool":      {{Command: "echo local", Network: &no}},
		"session_start": {{Command: "curl -H 'Authorization: Bearer abcdefghijklmnop1234567890' https://x.example/h"}},
	}}
	skills := []skill.Skill{
		{Name: "mine", Source: "global", Path: "/home/u/skills/mine"},
		{Name: "repo", Source: "project", Path: "/p/.janetcode/skills/repo", Files: []string{"run.sh"}},
		{Name: "evil", Source: "agents", Path: "/p/.agents/skills/evil", Blocked: true, Findings: []skill.Finding{{}}},
		{Name: "core", Source: "builtin"},
		{Name: "plug", Source: "plugin:acme", Path: "/c/plugins/acme/skills/plug"},
	}
	build := func(hooksOK, skillsOK bool) Report {
		return Build(context.Background(), Inputs{Cwd: "/p", Store: Store{Path: filepath.Join(t.TempDir(), FileName)},
			UserHooks: user, ProjectHooks: proj, HooksSource: "/home/u/config.json",
			ProjectHooksOK: Verdict{Trusted: hooksOK, Why: "project trust level full (stored)"},
			Skills:         skills, SkillTrust: Verdict{Trusted: skillsOK, Why: "project trust level instructions (stored)"}})
	}

	r := build(true, true)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the report ran a hook")
	}
	var active, inactive int
	for _, h := range r.Hooks {
		if h.Active {
			active++
		} else {
			inactive++
		}
		if h.Event == "session_start" {
			if h.Active || !h.Network || h.Trust.Status != StatusRestricted {
				t.Fatalf("network project hook = %+v", h)
			}
			if strings.Contains(h.Command, "abcdefghijklmnop") {
				t.Fatalf("hook command leaked a credential: %q", h.Command)
			}
		}
	}
	if active != 2 || inactive != 1 {
		t.Fatalf("active %d inactive %d: %+v", active, inactive, r.Hooks)
	}
	skillTrust := map[string]string{}
	for _, s := range r.Skills {
		skillTrust[s.Name] = s.Trust.Status
	}
	want := map[string]string{"mine": StatusTrusted, "repo": StatusTrusted, "evil": StatusRefused, "core": StatusTrusted, "plug": StatusTrusted}
	for k, v := range want {
		if skillTrust[k] != v {
			t.Fatalf("skill %s trust %q, want %q", k, skillTrust[k], v)
		}
	}

	r = build(false, false)
	for _, h := range r.Hooks {
		if h.Scope == ScopeProject && (h.Active || h.Trust.Status != StatusUntrusted) {
			t.Fatalf("untrusted project hook = %+v", h)
		}
		if h.Scope == ScopeUser && !h.Active {
			t.Fatal("the user's own hook is inactive")
		}
	}
	for _, s := range r.Skills {
		switch s.Name {
		case "repo", "plug":
			if s.Trust.Status != StatusUntrusted {
				t.Fatalf("%s = %+v", s.Name, s.Trust)
			}
		case "mine", "core":
			if s.Trust.Status != StatusTrusted {
				t.Fatalf("%s = %+v", s.Name, s.Trust)
			}
		}
	}
	if !strings.Contains(r.Text(), "will not run") {
		t.Fatalf("text:\n%s", r.Text())
	}
}

// The text and the JSON are two renderings of one report.
func TestTextAndJSONAgree(t *testing.T) {
	in := Inputs{Version: "v9", Cwd: "/p", Store: Store{Path: filepath.Join(t.TempDir(), FileName)},
		Global:  map[string]mcp.ServerConfig{"a": {Command: "x-none"}, "b": {URL: "https://h.example/mcp"}},
		Project: map[string]mcp.ServerConfig{"c": {Command: "x-none"}}, ProjectMCP: Verdict{Trusted: true}}
	r := Build(context.Background(), in)
	var back Report
	if err := json.Unmarshal([]byte(r.JSON()), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.MCP) != 3 || back.Version != "v9" {
		t.Fatalf("round trip = %+v", back)
	}
	text := back.Text()
	if text != r.Text() {
		t.Fatalf("text of the decoded JSON differs:\n%s\n---\n%s", text, r.Text())
	}
	for _, name := range []string{"a", "b", "c"} {
		if !strings.Contains(text, "  "+name+" · ") {
			t.Fatalf("%s missing from text", name)
		}
	}
	if !strings.Contains(text, "MCP servers, user-global") || !strings.Contains(text, "MCP servers, project-local") {
		t.Fatalf("scopes not separated:\n%s", text)
	}
}

func TestRedaction(t *testing.T) {
	for in, leak := range map[string]string{
		"https://user:pw123456@h.example/x":                 "pw123456",
		"https://h.example/mcp?api_key=KEY123456&a=b":       "KEY123456",
		"https://h.example/t/abcdEFGH1234abcdEFGH1234abcd9": "abcdEFGH1234abcdEFGH1234abcd9",
	} {
		out := RedactURL(in)
		if strings.Contains(out, leak) || !strings.Contains(out, "h.example") {
			t.Errorf("RedactURL(%q) = %q", in, out)
		}
	}
	if got := RedactURL("https://h.example/mcp?mode=fast"); got != "https://h.example/mcp?mode=fast" {
		t.Errorf("harmless URL changed: %q", got)
	}
	args := RedactArgs([]string{"serve", "--password", "p4ssw0rd!", "--secret=s3cr3t", "--port", "80", "DB_TOKEN=abc", "https://a:b@h.example"})
	joined := strings.Join(args, " ")
	for _, leak := range []string{"p4ssw0rd", "s3cr3t", "DB_TOKEN=abc", "a:b@"} {
		if strings.Contains(joined, leak) {
			t.Errorf("args leaked %q: %s", leak, joined)
		}
	}
	if !strings.Contains(joined, "--port 80") || !strings.Contains(joined, "serve") {
		t.Errorf("harmless args changed: %s", joined)
	}
}

// The grant store is on the protected list, so the model cannot edit its
// own restrictions.
func TestGrantStoreIsProtected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	found := false
	for _, p := range perm.NewProtected(nil).Patterns() {
		if strings.HasSuffix(p, string(filepath.Separator)+FileName) {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s is not among the protected paths", FileName)
	}
}
