package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// The report is one structure rendered three ways: the text a terminal
// or the web sheet shows (/doctor mcp, /inspect extensions), and JSON
// (--doctor mcp --output-format json, /inspect extensions json). All of
// them come from Build, so they cannot disagree.
//
// Building it never calls a tool, runs a hook or loads a skill's
// scripts. A running server is described from what it already told us;
// a server that is not running is probed only when the caller supplies a
// Probe and the server's grant allows it to connect, and a probe is the
// handshake plus the list requests (tools, prompts, resources) and
// nothing else. Every string that can carry a credential goes through
// the redaction in redact.go.

// Where an extension was declared.
const (
	ScopeUser    = "user-global"
	ScopeProject = "project-local"
)

// Trust statuses.
const (
	StatusTrusted    = "trusted"
	StatusRestricted = "restricted"
	StatusDisabled   = "disabled"
	StatusUntrusted  = "untrusted"
	StatusRefused    = "refused"
)

// TrustRow is whether an extension may act, and why.
type TrustRow struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// AuthRow says how a server is authenticated, without the credential.
type AuthRow struct {
	// Kind is none, env, headers, token_file or oauth.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// ToolRow is one tool of an MCP server.
type ToolRow struct {
	Name     string `json:"name"`
	ReadOnly bool   `json:"read_only"`
	// Deferred tools are loaded on demand through tool_search rather
	// than sent with every request.
	Deferred bool `json:"deferred"`
	// Callable is whether the grant lets the model call it now; Blocked
	// says what stops it when not.
	Callable bool   `json:"callable"`
	Blocked  string `json:"blocked,omitempty"`
}

// MCPRow is one MCP server.
type MCPRow struct {
	Name      string   `json:"name"`
	Scope     string   `json:"scope"`
	Source    string   `json:"source"`
	Shadowed  bool     `json:"shadowed,omitempty"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	// Executable is the resolved path, or "" when the command is not on
	// PATH (Found false).
	Executable string   `json:"executable,omitempty"`
	Found      bool     `json:"found"`
	URL        string   `json:"url,omitempty"`
	EnvNames   []string `json:"env,omitempty"`
	Auth       AuthRow  `json:"auth"`
	Trust      TrustRow `json:"trust"`
	// Capabilities are the scopes in force.
	Capabilities  []string `json:"capabilities"`
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// State is running, failed, starting, down, probed, not started or
	// shadowed; StateDetail has the reason.
	State       string    `json:"state"`
	StateDetail string    `json:"state_detail,omitempty"`
	Tools       []ToolRow `json:"tools"`
	Prompts     []string  `json:"prompts"`
	Resources   *int      `json:"resources,omitempty"`
}

// HookRow is one configured hook; it is described, never run.
type HookRow struct {
	Event   string   `json:"event"`
	Match   string   `json:"match,omitempty"`
	Command string   `json:"command"`
	Network bool     `json:"network"`
	Scope   string   `json:"scope"`
	Source  string   `json:"source"`
	Trust   TrustRow `json:"trust"`
	// Active is whether it will run: a project hook only when the file
	// is approved and it does not reach the network.
	Active bool `json:"active"`
}

// SkillRow is one skill; its files are never executed here.
type SkillRow struct {
	Name     string   `json:"name"`
	Scope    string   `json:"scope"`
	Source   string   `json:"source"`
	Path     string   `json:"path"`
	Trust    TrustRow `json:"trust"`
	Findings int      `json:"scan_findings"`
	Blocked  bool     `json:"blocked"`
	Scripts  int      `json:"supporting_files"`
}

// Report is the whole answer.
type Report struct {
	Version string     `json:"version"`
	Cwd     string     `json:"cwd"`
	Notes   []string   `json:"notes,omitempty"`
	MCP     []MCPRow   `json:"mcp"`
	Hooks   []HookRow  `json:"hooks"`
	Skills  []SkillRow `json:"skills"`
}

// Verdict is a trust decision made elsewhere (the project gate), with
// the sentence that explains it.
type Verdict struct {
	Trusted bool
	Why     string
}

// LiveServer is what a running session knows about one server.
type LiveServer struct {
	// State is running, starting, failed or down.
	State  string
	Err    error
	Client *mcp.Client
}

// Inputs is everything Build reads, supplied by the caller so the same
// builder serves a running session and a bare command line.
type Inputs struct {
	Version    string
	Cwd        string
	ConfigPath string
	Global     map[string]mcp.ServerConfig
	Project    map[string]mcp.ServerConfig
	// ProjectMCP is whether the project's .mcp.json may start servers.
	ProjectMCP Verdict
	Store      Store
	Tokens     *mcp.TokenStore
	// Live describes a server of the running session; nil, or false,
	// means it is not running.
	Live func(name string) (LiveServer, bool)
	// Probe starts a server that is not running, for its handshake and
	// lists. Nil means no probing. The caller closes nothing: Build
	// closes the client it is given.
	Probe func(ctx context.Context, name string, cfg mcp.ServerConfig) (*mcp.Client, error)
	// DeferAbove is the tool count past which a server's tools are
	// deferred (the manager's rule).
	DeferAbove int
	Now        time.Time

	UserHooks      hook.Config
	ProjectHooks   hook.Config
	ProjectHooksOK Verdict
	HooksSource    string // the user's config path, for the source column

	Skills []skill.Skill
	// SkillTrust is the verdict on skills the repository supplied.
	SkillTrust Verdict
}

// Build assembles the report.
func Build(ctx context.Context, in Inputs) Report {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	deferAbove := in.DeferAbove
	if deferAbove <= 0 {
		deferAbove = 6 // mcptool.DefaultDeferAbove
	}
	r := Report{Version: in.Version, Cwd: in.Cwd, MCP: []MCPRow{}, Hooks: []HookRow{}, Skills: []SkillRow{}}
	if _, err := in.Store.load(); err != nil {
		r.Notes = append(r.Notes, "the extension grant store cannot be read, so every server with a recorded grant is refused: "+err.Error())
	}
	pol := Policy{Store: in.Store, Cwd: in.Cwd}

	for _, name := range sortedKeys(in.Global) {
		_, shadowed := in.Project[name]
		shadowed = shadowed && in.ProjectMCP.Trusted
		r.MCP = append(r.MCP, mcpRow(ctx, in, pol, name, in.Global[name], false, shadowed, deferAbove, now))
	}
	for _, name := range sortedKeys(in.Project) {
		r.MCP = append(r.MCP, mcpRow(ctx, in, pol, name, in.Project[name], true, false, deferAbove, now))
	}
	r.Hooks = hookRows(in)
	r.Skills = skillRows(in)
	return r
}

func sortedKeys(m map[string]mcp.ServerConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// effective works out the grant in force for one row and the trust
// statement that goes with it. It is the same rule the policy applies
// at call time, plus the project-file approval that decides whether a
// project server was started at all.
func effective(in Inputs, pol Policy, name string, project bool) (Grant, TrustRow) {
	var key string
	if project {
		key = ProjectKey(in.Cwd, name)
	} else {
		key = UserKey(name)
	}
	if project && !in.ProjectMCP.Trusted {
		return Grant{}, TrustRow{StatusUntrusted, orElse(in.ProjectMCP.Why, "the project's .mcp.json is not approved") + "; the server is not started"}
	}
	g, ok, err := in.Store.Get(key)
	switch {
	case err != nil:
		return Grant{}, TrustRow{StatusRefused, "grant store unreadable: " + err.Error()}
	case ok:
		why := orElse(g.Reason, "recorded grant")
		switch {
		case !g.Has(Connect):
			return g, TrustRow{StatusDisabled, why}
		case !g.Has(Mutate):
			return g, TrustRow{StatusRestricted, why}
		}
		return g, TrustRow{StatusTrusted, why}
	}
	if project {
		return Full(""), TrustRow{StatusTrusted, orElse(in.ProjectMCP.Why, "the project's .mcp.json is approved")}
	}
	return Full(""), TrustRow{StatusTrusted, "declared in your own config file (" + orElse(in.ConfigPath, "config.json") + "), so trusted by authorship"}
}

func orElse(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func mcpRow(ctx context.Context, in Inputs, pol Policy, name string, cfg mcp.ServerConfig, project, shadowed bool, deferAbove int, now time.Time) MCPRow {
	row := MCPRow{Name: name, Tools: []ToolRow{}, Prompts: []string{}, Capabilities: []string{}}
	if project {
		row.Scope, row.Source = ScopeProject, mcp.ProjectFile+" in "+in.Cwd
	} else {
		row.Scope, row.Source = ScopeUser, orElse(in.ConfigPath, "config.json")
	}
	describeTransport(&row, cfg)
	row.Auth = authRow(in, name, cfg, now)
	g, trust := effective(in, pol, name, project)
	row.Trust = trust
	for _, s := range g.Scopes {
		row.Capabilities = append(row.Capabilities, string(s))
	}
	row.DisabledTools = g.DisabledTools

	if shadowed {
		row.Shadowed = true
		row.State, row.StateDetail = "shadowed", "the project's entry of the same name runs instead"
		return row
	}
	var client *mcp.Client
	owned := false
	switch {
	case trust.Status == StatusUntrusted || trust.Status == StatusRefused || trust.Status == StatusDisabled:
		row.State, row.StateDetail = "not started", "not allowed to connect: "+trust.Reason
		return row
	}
	if in.Live != nil {
		if lv, ok := in.Live(name); ok {
			row.State = lv.State
			if lv.Err != nil {
				row.StateDetail = RedactText(strings.TrimSpace(lv.Err.Error()))
			}
			client = lv.Client
		}
	}
	if client == nil && row.State == "" {
		switch {
		case in.Probe == nil:
			row.State, row.StateDetail = "not started", "not running in this session and not probed"
		case cfg.URL == "" && !row.Found:
			row.State, row.StateDetail = "not started", "command "+cfg.Command+" is not on PATH, so it cannot be probed"
		default:
			c, err := in.Probe(ctx, name, cfg)
			if err != nil {
				row.State, row.StateDetail = "failed", "probe: "+RedactText(strings.TrimSpace(err.Error()))
			} else {
				client, owned = c, true
				row.State = "probed"
				row.StateDetail = "started for the report: handshake and list requests only, no tool was called, then stopped"
			}
		}
	}
	if client == nil {
		return row
	}
	if owned {
		defer client.Close()
	}
	listing(ctx, &row, client, g, trust.Status != StatusUntrusted, deferAbove)
	return row
}

// listing fills the tools, prompts and resources from a client that
// already answered its handshake. The only requests are lists.
func listing(ctx context.Context, row *MCPRow, c *mcp.Client, g Grant, granted bool, deferAbove int) {
	infos := c.Tools()
	count := len(infos)
	if c.Capabilities.Resources {
		count += 2 // list_resources and read_resource are tools too
	}
	deferred := count > deferAbove
	for _, info := range infos {
		ro := info.Annotations.ReadOnlyHint != nil && *info.Annotations.ReadOnlyHint
		t := ToolRow{Name: info.Name, ReadOnly: ro, Deferred: deferred, Callable: true}
		if err := g.Allows(row.Name, info.Name, !ro); err != nil || !granted {
			t.Callable = false
			if err != nil {
				t.Blocked = err.Error()
			}
		}
		row.Tools = append(row.Tools, t)
	}
	sort.Slice(row.Tools, func(i, j int) bool { return row.Tools[i].Name < row.Tools[j].Name })
	for _, p := range c.Prompts() {
		row.Prompts = append(row.Prompts, p.Name)
	}
	sort.Strings(row.Prompts)
	if c.Capabilities.Resources && g.Has(Read) {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if res, err := c.ListResources(rctx); err == nil {
			n := len(res)
			row.Resources = &n
		}
	}
}

func describeTransport(row *MCPRow, cfg mcp.ServerConfig) {
	for k := range cfg.Env {
		row.EnvNames = append(row.EnvNames, k)
	}
	sort.Strings(row.EnvNames)
	if cfg.URL != "" {
		row.Transport = cfg.Type
		if row.Transport == "" {
			row.Transport = "http"
		}
		row.URL = RedactURL(cfg.URL)
		row.Found = true
		return
	}
	row.Transport = "stdio"
	row.Command = cfg.Command
	row.Args = RedactArgs(cfg.Args)
	if p, err := exec.LookPath(cfg.Command); err == nil {
		row.Executable, row.Found = p, true
	}
}

func authRow(in Inputs, name string, cfg mcp.ServerConfig, now time.Time) AuthRow {
	if cfg.URL == "" {
		if len(cfg.Env) > 0 {
			return AuthRow{"env", "environment: " + strings.Join(envNames(cfg), ", ") + " (values hidden)"}
		}
		return AuthRow{"none", "local process, nothing to authenticate"}
	}
	switch {
	case cfg.TokenFile != "":
		detail := "bearer token read from " + cfg.TokenFile + " on every request"
		if _, err := os.Stat(expandHome(cfg.TokenFile)); err != nil && !strings.Contains(cfg.TokenFile, "$") {
			detail += " (file not readable: " + textfmt.Clip(err.Error(), 80) + ")"
		}
		return AuthRow{"token_file", detail}
	case len(cfg.Headers) > 0:
		names := make([]string, 0, len(cfg.Headers))
		for k := range cfg.Headers {
			names = append(names, k)
		}
		sort.Strings(names)
		return AuthRow{"headers", "headers: " + strings.Join(names, ", ") + " (values hidden)"}
	}
	if t, ok := in.Tokens.Get(name, cfg.URL); ok {
		return AuthRow{"oauth", oauthDetail(t, now)}
	}
	hint := "no login stored; /mcp auth " + name + " logs in if the server asks for it"
	return AuthRow{"none", hint}
}

func oauthDetail(t mcp.Token, now time.Time) string {
	var b strings.Builder
	b.WriteString("OAuth login stored")
	switch {
	case t.Expiry.IsZero():
		b.WriteString(", no expiry recorded")
	case t.Expiry.After(now):
		b.WriteString(", access token valid for " + textfmt.Age(t.Expiry.Sub(now)))
	default:
		b.WriteString(", access token expired " + textfmt.Age(now.Sub(t.Expiry)) + " ago")
		if t.RefreshToken != "" {
			b.WriteString(" (a refresh token is stored, so it renews on the next call)")
		} else {
			b.WriteString(" and there is no refresh token: /mcp auth to log in again")
		}
	}
	if t.Scope != "" {
		b.WriteString(" · scope: " + t.Scope)
	}
	if t.RequiredScope != "" {
		b.WriteString(" · the server wants more: " + t.RequiredScope)
	}
	return b.String()
}

func envNames(cfg mcp.ServerConfig) []string {
	var out []string
	for k := range cfg.Env {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}

func hookRows(in Inputs) []HookRow {
	rows := []HookRow{}
	add := func(cfg hook.Config, scope, source string, project bool, v Verdict) {
		events := make([]string, 0, len(cfg.Hooks))
		for ev := range cfg.Hooks {
			events = append(events, ev)
		}
		sort.Strings(events)
		for _, ev := range events {
			for _, h := range cfg.Hooks[ev] {
				row := HookRow{Event: ev, Match: h.Match, Command: textfmt.Clip(RedactText(h.Command), 120),
					Network: h.ReachesNetwork(), Scope: scope, Source: source}
				switch {
				case !project:
					row.Trust = TrustRow{StatusTrusted, "from your own config file, trusted by authorship"}
					row.Active = true
				case !v.Trusted:
					row.Trust = TrustRow{StatusUntrusted, orElse(v.Why, "the project's hooks file is not approved") + "; it does not run"}
				case h.ReachesNetwork():
					row.Trust = TrustRow{StatusRestricted, "approved file, but a project hook may not reach the network, so this one does not run"}
				default:
					row.Trust = TrustRow{StatusTrusted, orElse(v.Why, "the project's hooks file is approved") + "; local-only, timeout capped"}
					row.Active = true
				}
				rows = append(rows, row)
			}
		}
	}
	add(in.UserHooks, ScopeUser, orElse(in.HooksSource, "config.json"), false, Verdict{})
	add(in.ProjectHooks, ScopeProject, hook.ProjectFile+" in "+in.Cwd, true, in.ProjectHooksOK)
	return rows
}

func skillRows(in Inputs) []SkillRow {
	rows := []SkillRow{}
	for _, s := range in.Skills {
		scope := ScopeUser
		repo := s.Source == "project" || s.Source == "agents" || s.Source == "claude"
		if repo {
			scope = ScopeProject
		}
		plugin := strings.HasPrefix(s.Source, "plugin:")
		row := SkillRow{Name: s.Name, Scope: scope, Source: s.Source, Path: s.Path, Findings: len(s.Findings), Blocked: s.Blocked, Scripts: len(s.Files)}
		switch {
		case s.Source == "builtin":
			row.Trust = TrustRow{StatusTrusted, "ships inside the janetcode binary"}
		case !repo && !plugin:
			row.Trust = TrustRow{StatusTrusted, "from your own skills directory, trusted by authorship"}
		case s.Blocked:
			row.Trust = TrustRow{StatusRefused, "the repository supplied it and its scan found a critical problem; it is not attached or offered"}
		case !in.SkillTrust.Trusted:
			row.Trust = TrustRow{StatusUntrusted, orElse(in.SkillTrust.Why, "the project is not trusted for instructions") + "; it is not attached"}
		default:
			row.Trust = TrustRow{StatusTrusted, orElse(in.SkillTrust.Why, "the project is trusted for instructions")}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// JSON renders the report as indented JSON.
func (r Report) JSON() string {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(b)
}

// Text renders the report for a terminal or a sheet.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "extensions · janetcode %s · %s\n", r.Version, r.Cwd)
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	for _, scope := range []string{ScopeUser, ScopeProject} {
		fmt.Fprintf(&b, "\nMCP servers, %s\n", scope)
		n := 0
		for _, m := range r.MCP {
			if m.Scope != scope {
				continue
			}
			n++
			m.text(&b)
		}
		if n == 0 {
			b.WriteString("  none\n")
		}
	}
	b.WriteString("\nHooks (described, never run by this report)\n")
	if len(r.Hooks) == 0 {
		b.WriteString("  none\n")
	}
	for _, h := range r.Hooks {
		run := "will run"
		if !h.Active {
			run = "will not run"
		}
		net := ""
		if h.Network {
			net = " · reaches the network"
		}
		fmt.Fprintf(&b, "  %-14s %s%s · %s · %s\n", h.Event, h.Command, net, h.Scope, run)
		fmt.Fprintf(&b, "      %s: %s\n", h.Trust.Status, h.Trust.Reason)
	}
	b.WriteString("\nSkills\n")
	if len(r.Skills) == 0 {
		b.WriteString("  none\n")
	}
	for _, s := range r.Skills {
		extra := ""
		if s.Findings > 0 {
			extra = fmt.Sprintf(" · %s from the scan", textfmt.Plural(s.Findings, "finding"))
		}
		fmt.Fprintf(&b, "  %-22s %s · %s%s\n", s.Name, s.Scope, s.Source, extra)
		fmt.Fprintf(&b, "      %s: %s\n", s.Trust.Status, s.Trust.Reason)
	}
	b.WriteString("\ncontrols: /mcp enable <name> [connect|read|mutate|all] · /mcp disable <name> [capability] · /mcp tool <name> <tool> on|off · /trust for the project's hooks and skills\n")
	return b.String()
}

func (m MCPRow) text(b *strings.Builder) {
	fmt.Fprintf(b, "  %s · %s · %s\n", m.Name, m.Transport, m.State)
	fmt.Fprintf(b, "      source: %s\n", m.Source)
	if m.Transport == "stdio" {
		exe := m.Executable
		if !m.Found {
			exe = m.Command + " (NOT FOUND on PATH)"
		}
		fmt.Fprintf(b, "      executable: %s", exe)
		if len(m.Args) > 0 {
			fmt.Fprintf(b, " %s", strings.Join(m.Args, " "))
		}
		b.WriteString("\n")
	} else {
		fmt.Fprintf(b, "      url: %s\n", m.URL)
	}
	fmt.Fprintf(b, "      auth: %s\n", m.Auth.Detail)
	fmt.Fprintf(b, "      trust: %s · %s\n", m.Trust.Status, m.Trust.Reason)
	caps := strings.Join(m.Capabilities, ", ")
	if caps == "" {
		caps = "none"
	}
	fmt.Fprintf(b, "      capabilities: %s", caps)
	if len(m.DisabledTools) > 0 {
		fmt.Fprintf(b, " · tools off: %s", strings.Join(m.DisabledTools, ", "))
	}
	b.WriteString("\n")
	if m.StateDetail != "" {
		fmt.Fprintf(b, "      %s\n", m.StateDetail)
	}
	if len(m.Tools) > 0 {
		fmt.Fprintf(b, "      tools (%d):\n", len(m.Tools))
		for _, t := range m.Tools {
			kind := "may change things"
			if t.ReadOnly {
				kind = "read-only"
			}
			flags := ""
			if t.Deferred {
				flags += " · deferred"
			}
			if !t.Callable {
				flags += " · BLOCKED"
			}
			fmt.Fprintf(b, "        %-26s %s%s\n", t.Name, kind, flags)
		}
	}
	if len(m.Prompts) > 0 {
		fmt.Fprintf(b, "      prompts: %s\n", strings.Join(m.Prompts, ", "))
	}
	if m.Resources != nil {
		fmt.Fprintf(b, "      resources: %d\n", *m.Resources)
	}
}
