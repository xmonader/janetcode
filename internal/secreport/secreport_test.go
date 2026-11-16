package secreport

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/mcp"
)

func find(r Report, area, name string) Check {
	for _, c := range r.Checks {
		if c.Area == area && c.Name == name {
			return c
		}
	}
	return Check{}
}

func noBackend() (func(string) (string, error), func() int) {
	return func(string) (string, error) { return "", errors.New("not found") }, func() int { return 0 }
}

func TestNoCredentialIsEverPrinted(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"x":"CONFIGFILESECRET99"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Provider: "main", Model: "m",
		Providers: map[string]config.Provider{
			"main": {Type: "openai", BaseURL: "https://user:URLPASSWORD123@api.example.test/v1?api_key=QUERYSECRET456&mode=x", APIKey: "sk-LITERALKEYSECRET1234567890", APIKeys: []string{"SECONDKEYSECRET7890abcdef"}},
			"viaop": {Type: "openai", BaseURL: "https://op.example.test/v1", APIKey: "op://vault/item/SECRETFIELD"},
		},
		Search:    cfgSearch("SEARCHKEYSECRET123"),
		Decisions: &config.DecisionsConfig{BaseURL: "https://dec.example.test/v1?token=DECTOKEN789", Model: "m"},
	}
	getenv := func(k string) string {
		switch k {
		case "ANTHROPIC_API_KEY":
			return "ENVSECRETANTHROPIC123456"
		case "OPENROUTER_API_KEY":
			return "ENVSECRETOPENROUTER123"
		}
		return ""
	}
	in := Inputs{Version: "v1", Cfg: cfg, CfgPath: cfgPath, DataDir: dir, Getenv: getenv, TrustStore: filepath.Join(dir, "mcp-trust.json"),
		CfgErr: errors.New("config: bad value in header Authorization: Bearer abcdefghijklmnop1234567890XYZ"),
		Extensions: extension.Report{MCP: []extension.MCPRow{
			{Name: "r", Scope: extension.ScopeUser, URL: extension.RedactURL("https://mcp.example.test/x?access_token=MCPQUERYSECRET1"), Trust: extension.TrustRow{Status: extension.StatusTrusted}},
		}}}
	rep := Build(in)
	all := rep.Text() + rep.JSON()
	for _, leak := range []string{"CONFIGFILESECRET", "URLPASSWORD", "QUERYSECRET", "LITERALKEYSECRET", "SECONDKEYSECRET", "SECRETFIELD", "SEARCHKEYSECRET", "DECTOKEN", "ENVSECRET", "abcdefghijklmnop1234567890XYZ", "MCPQUERYSECRET"} {
		if strings.Contains(all, leak) {
			t.Fatalf("the report printed %q:\n%s", leak, rep.Text())
		}
	}
	// What it does say: where the key comes from, never its value.
	if c := find(rep, "providers", "provider main"); !strings.Contains(c.Detail, "the config file") || !strings.Contains(c.Detail, "2 keys") || !strings.Contains(c.Detail, "api.example.test") {
		t.Fatalf("main = %+v", c)
	}
	if c := find(rep, "providers", "provider viaop"); !strings.Contains(c.Detail, "1Password reference") {
		t.Fatalf("viaop = %+v", c)
	}
	if c := find(rep, "providers", "credential variables set"); !strings.Contains(c.Detail, "ANTHROPIC_API_KEY") {
		t.Fatalf("env names = %+v", c)
	}
	if c := find(rep, "privacy", "decision service"); !strings.Contains(c.Detail, "OPENROUTER_API_KEY") {
		t.Fatalf("decisions = %+v", c)
	}
}

func TestSandboxStatesAreDistinct(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the probe doubles stand in for the Linux backends")
	}
	cfg := func(profile string) *config.Config { return &config.Config{Sandbox: profile} }
	none := func() Inputs {
		look, abi := noBackend()
		return Inputs{Sandbox: shellProbe(look, abi)}
	}
	docker := func() Inputs {
		return Inputs{Sandbox: shellProbe(func(n string) (string, error) {
			if n == "docker" {
				return "/usr/bin/docker", nil
			}
			return "", errors.New("not found")
		}, func() int { return 0 })}
	}
	for _, tc := range []struct {
		name  string
		in    Inputs
		state string
		want  string
		fail  bool
	}{
		{"machine can, nobody asked", with(docker(), cfg(""), false), "disabled-by-choice", "OFF BY CHOICE", false},
		{"machine cannot, nobody asked", with(none(), cfg(""), false), "unavailable", "UNAVAILABLE ON THIS MACHINE", false},
		{"machine can, asked", with(docker(), cfg("workspace"), false), "enabled", "on (workspace profile", false},
		{"machine cannot, asked by flag", with(none(), cfg(""), true), "requested-unavailable", "was requested but this machine has no backend", true},
		{"machine cannot, asked by config", with(none(), cfg("strict"), false), "requested-unavailable", "strict profile was requested", true},
		{"off in the config is a choice", with(docker(), cfg("off"), false), "disabled-by-choice", "OFF BY CHOICE", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Build(tc.in)
			if rep.Sandbox.State != tc.state {
				t.Fatalf("state = %q, want %q", rep.Sandbox.State, tc.state)
			}
			c := find(rep, "sandbox", "state")
			if !strings.Contains(c.Detail, tc.want) {
				t.Fatalf("detail = %q, want %q", c.Detail, tc.want)
			}
			if (c.Status == Fail) != tc.fail {
				t.Fatalf("status = %s", c.Status)
			}
			if tc.fail && rep.Failed == 0 {
				t.Fatal("a requested sandbox the machine cannot provide did not fail the report")
			}
			if len(rep.Sandbox.Backends) == 0 || rep.Sandbox.Host == "" {
				t.Fatalf("sandbox = %+v", rep.Sandbox)
			}
		})
	}
	// Unavailable backends say why, individually.
	rep := Build(none())
	for _, b := range rep.Sandbox.Backends {
		if b.Available || b.Why == "" {
			t.Errorf("backend %+v on a machine with none", b)
		}
	}
	if c := find(rep, "sandbox", "backend landlock"); c.Status != Info || !strings.Contains(c.Detail, "not available") {
		t.Fatalf("landlock row = %+v", c)
	}
}

func with(in Inputs, cfg *config.Config, flag bool) Inputs {
	in.Cfg, in.SandboxFlag = cfg, flag
	return in
}

func TestBinaryProvenance(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "janetcode")
	if err := os.WriteFile(exe, []byte("binary bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	info := func(modified string, rev string) *debug.BuildInfo {
		bi := &debug.BuildInfo{GoVersion: "go1.25.4", Deps: []*debug.Module{{}, {}}}
		for k, v := range map[string]string{"vcs.revision": rev, "vcs.modified": modified, "vcs.time": "2026-11-16T10:00:00Z", "-trimpath": "true", "CGO_ENABLED": "0", "GOOS": "linux", "GOARCH": "amd64"} {
			if v != "" {
				bi.Settings = append(bi.Settings, debug.BuildSetting{Key: k, Value: v})
			}
		}
		return bi
	}
	rep := Build(Inputs{Version: "v0.3.0", Exe: exe, BuildInfo: info("false", "abc123")})
	if rep.Binary.Commit != "abc123" || rep.Binary.Modified || !rep.Binary.Trimpath || rep.Binary.Deps != 2 || rep.Binary.CGO != "0" {
		t.Fatalf("binary = %+v", rep.Binary)
	}
	// sha256("binary bytes")
	if len(rep.Binary.SHA256) != 64 {
		t.Fatalf("sha256 = %q", rep.Binary.SHA256)
	}
	if c := find(rep, "binary", "source commit"); c.Status != OK || !strings.Contains(c.Detail, "abc123") {
		t.Fatalf("clean = %+v", c)
	}
	if c := find(rep, "binary", "source commit"); !strings.Contains(c.Detail, "clean checkout") {
		t.Fatalf("clean = %+v", c)
	}
	rep = Build(Inputs{Version: "v0.3.0", BuildInfo: info("true", "abc123")})
	if c := find(rep, "binary", "source commit"); c.Status != Warn || !strings.Contains(c.Detail, "not the published source") {
		t.Fatalf("modified = %+v", c)
	}
	rep = Build(Inputs{Version: "dev", BuildInfo: info("", "")})
	if c := find(rep, "binary", "source commit"); c.Status != Warn || !strings.Contains(c.Detail, "no VCS stamp") {
		t.Fatalf("unstamped = %+v", c)
	}
	rep = Build(Inputs{Version: "dev", Exe: filepath.Join(t.TempDir(), "missing")})
	if c := find(rep, "binary", "checksum"); c.Status != Warn {
		t.Fatalf("missing exe = %+v", c)
	}
}

func TestTrustStoreChecks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful")
	}
	dir := t.TempDir()
	store := filepath.Join(dir, "mcp-trust.json")
	write := func(name, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("mcp-trust.json", `{"/p#project":"full","/q#project":"instructions","/p#hooks":"h","/p":"x"}`, 0o600)
	write("mcp-auth.json", `{}`, 0o644)
	rep := Build(Inputs{TrustStore: store, CfgPath: filepath.Join(dir, "config.json")})
	if c := find(rep, "trust", "project approvals"); c.Status != OK {
		t.Fatalf("approvals = %+v", c)
	}
	if c := find(rep, "trust", "recorded approvals"); !strings.Contains(c.Detail, "1 project level full") || !strings.Contains(c.Detail, "1 project level instructions") || !strings.Contains(c.Detail, "1 per-file hooks") || !strings.Contains(c.Detail, "1 per-file mcp") {
		t.Fatalf("counts = %+v", c)
	}
	if c := find(rep, "trust", "full trust"); !strings.Contains(c.Detail, "1 project trusted at level full") {
		t.Fatalf("full = %+v", c)
	}
	if c := find(rep, "trust", "MCP OAuth logins"); c.Status != Warn || !strings.Contains(c.Detail, "readable by others") {
		t.Fatalf("oauth file = %+v", c)
	}
	if c := find(rep, "trust", "extension grants"); c.Status != Info {
		t.Fatalf("absent grants = %+v", c)
	}

	// Writable by others: anyone could approve their own files.
	write("mcp-trust.json", `{}`, 0o666)
	rep = Build(Inputs{TrustStore: store})
	if c := find(rep, "trust", "project approvals"); c.Status != Fail || !strings.Contains(c.Detail, "writable by others") || rep.Failed == 0 {
		t.Fatalf("world-writable = %+v failed=%d", c, rep.Failed)
	}

	// Corrupt: every approval is ignored, and the report says so.
	write("mcp-trust.json", `{broken`, 0o600)
	rep = Build(Inputs{TrustStore: store})
	if c := find(rep, "trust", "approvals readable"); c.Status != Fail || !strings.Contains(c.Detail, "not valid JSON") {
		t.Fatalf("corrupt = %+v", c)
	}
}

func TestProviderEndpointChecks(t *testing.T) {
	cfg := &config.Config{Provider: "remote", Providers: map[string]config.Provider{
		"remote": {Type: "openai", BaseURL: "http://gateway.example.test/v1", APIKey: "k1"},
		"local":  {Type: "openai", BaseURL: "http://127.0.0.1:11434/v1"},
		"nokey":  {Type: "openai", BaseURL: "http://other.example.test/v1"},
		"plain":  {Type: "anthropic", BaseURL: "https://api.anthropic.com", KeyEnv: "MY_KEY"},
	}}
	rep := Build(Inputs{Cfg: cfg, Getenv: func(string) string { return "" }})
	if c := find(rep, "providers", "provider remote"); c.Status != Fail || !strings.Contains(c.Detail, "PLAIN HTTP") || !strings.Contains(c.Detail, "selected") {
		t.Fatalf("remote = %+v", c)
	}
	if c := find(rep, "providers", "provider local"); c.Status != OK || !strings.Contains(c.Detail, "no key needed") {
		t.Fatalf("local = %+v", c)
	}
	if c := find(rep, "providers", "provider nokey"); c.Status != Warn {
		t.Fatalf("nokey = %+v", c)
	}
	if rep.Failed != 1 {
		t.Fatalf("failed = %d", rep.Failed)
	}
}

func TestMCPAndPrivacySummary(t *testing.T) {
	no := false
	cfg := &config.Config{UpdateCheck: &no, Egress: &config.EgressConfig{Default: "deny", Allow: []string{"github.com"}}}
	ext := extension.Report{
		MCP: []extension.MCPRow{
			{Name: "a", Scope: extension.ScopeUser, Trust: extension.TrustRow{Status: extension.StatusTrusted}},
			{Name: "b", Scope: extension.ScopeProject, URL: "http://remote.example.test/mcp", Trust: extension.TrustRow{Status: extension.StatusUntrusted}},
			{Name: "c", Scope: extension.ScopeProject, Trust: extension.TrustRow{Status: extension.StatusRestricted}},
		},
		Hooks: []extension.HookRow{{Active: true, Network: true}, {Active: false, Network: true}, {Active: true}},
	}
	rep := Build(Inputs{Cfg: cfg, Extensions: ext, DataDir: "/data/janetcode"})
	if c := find(rep, "mcp", "servers"); !strings.Contains(c.Detail, "1 user-global, 2 project-local, 1 remote · 1 untrusted") || !strings.Contains(c.Detail, "1 restricted") {
		t.Fatalf("servers = %+v", c)
	}
	if c := find(rep, "mcp", "server b"); c.Status != Warn {
		t.Fatalf("plain-http remote = %+v", c)
	}
	if c := find(rep, "mcp", "hooks"); !strings.Contains(c.Detail, "2 active, 1 of them reach the network") {
		t.Fatalf("hooks = %+v", c)
	}
	if c := find(rep, "privacy", "update check"); !strings.HasPrefix(c.Detail, "off") {
		t.Fatalf("update check = %+v", c)
	}
	if c := find(rep, "privacy", "telemetry"); c.Status != OK || !strings.Contains(c.Detail, "none") {
		t.Fatalf("telemetry = %+v", c)
	}
	if c := find(rep, "privacy", "egress policy"); c.Status != OK {
		t.Fatalf("egress = %+v", c)
	}
	if c := find(rep, "privacy", "retention"); !strings.Contains(c.Detail, "/data/janetcode") || !strings.Contains(c.Detail, "no automatic expiry") {
		t.Fatalf("retention = %+v", c)
	}
}

func TestTextAndJSON(t *testing.T) {
	rep := Build(Inputs{Version: "v1", Cfg: &config.Config{}})
	var back Report
	if err := json.Unmarshal([]byte(rep.JSON()), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Checks) != len(rep.Checks) || back.Sandbox.State != rep.Sandbox.State {
		t.Fatalf("round trip lost data: %+v", back)
	}
	text := rep.Text()
	for _, want := range []string{"Binary", "Sandbox", "Providers", "Mcp", "Privacy", "no security check failed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text lacks %q:\n%s", want, text)
		}
	}
	_ = mcp.ProjectFile
}
