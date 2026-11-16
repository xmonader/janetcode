// Package secreport is what `janetcode --doctor security` and `/doctor
// security` print: the facts about this machine and this installation
// that decide how far janetcode can be trusted with it. The local
// binary (where it came from and whether it was built from clean
// source), the sandbox (what the machine can do, and whether it is off
// by choice or off because it cannot be on), the trust stores, every
// endpoint a provider or a feature sends data to, the MCP configuration,
// and the privacy settings in force.
//
// A report names where a credential comes from (an environment variable
// by name, the config file, a 1Password reference) and never prints one:
// not a key, not a token, not a URL's userinfo or query. The tests plant
// a sentinel in every place a secret can sit and fail if it comes out.
package secreport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Statuses of a check.
const (
	OK   = "ok"
	Warn = "warn"
	Fail = "fail"
	Info = "info"
)

// Check is one line of the report.
type Check struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// Sandbox is the machine-readable answer to "is bash confined?". State
// is one of: enabled, disabled-by-choice, unavailable (the machine has
// no backend, and none was asked for), requested-unavailable (one was
// asked for and the machine cannot provide it).
type Sandbox struct {
	State     string                    `json:"state"`
	Requested bool                      `json:"requested"`
	Profile   string                    `json:"profile,omitempty"`
	Auto      string                    `json:"auto_backend,omitempty"`
	Backends  []shell.BackendCapability `json:"backends"`
	Host      string                    `json:"host"`
}

// Binary describes the executable that is running.
type Binary struct {
	Version  string `json:"version"`
	Path     string `json:"path,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Time     string `json:"commit_time,omitempty"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CGO      string `json:"cgo,omitempty"`
	Trimpath bool   `json:"trimpath"`
	Deps     int    `json:"module_dependencies"`
}

// Report is the whole answer.
type Report struct {
	Binary  Binary  `json:"binary"`
	Sandbox Sandbox `json:"sandbox"`
	Checks  []Check `json:"checks"`
	// Failed counts the checks that failed; the command's exit status.
	Failed int `json:"failed"`
}

// Inputs is what the report reads, supplied by the caller so a test can
// stand in for the machine.
type Inputs struct {
	Version string
	Cfg     *config.Config
	// CfgErr is what loading the configuration said; a missing
	// credential is not a reason to hide the rest of the report.
	CfgErr    error
	CfgPath   string
	DataDir   string
	Getenv    func(string) string
	Exe       string
	BuildInfo *debug.BuildInfo
	// Sandbox is probed for capabilities; SandboxFlag is --sandbox.
	Sandbox     *shell.Sandbox
	SandboxFlag bool
	// SandboxOn is a live session's sandbox switch, which /sandbox may
	// have turned on after the config and flags were read.
	SandboxOn bool
	// Extensions is the extension report, summarised here.
	Extensions extension.Report
	// TrustStore is the project-approval file (mcp-trust.json).
	TrustStore string
}

// Build assembles the report.
func Build(in Inputs) Report {
	if in.Getenv == nil {
		in.Getenv = os.Getenv
	}
	r := Report{}
	b := &builder{in: in, r: &r}
	b.binary()
	b.sandbox()
	b.trustStores()
	b.providers()
	b.mcp()
	b.privacy()
	for _, c := range r.Checks {
		if c.Status == Fail {
			r.Failed++
		}
	}
	return r
}

type builder struct {
	in Inputs
	r  *Report
}

func (b *builder) add(area, name, status, detail string) {
	b.r.Checks = append(b.r.Checks, Check{Area: area, Name: name, Status: status, Detail: detail})
}

func setting(info *debug.BuildInfo, key string) string {
	if info == nil {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

func (b *builder) binary() {
	in := b.in
	bin := Binary{Version: in.Version}
	if info := in.BuildInfo; info != nil {
		bin.Go = info.GoVersion
		bin.Commit = setting(info, "vcs.revision")
		bin.Time = setting(info, "vcs.time")
		bin.Modified = setting(info, "vcs.modified") == "true"
		bin.CGO = setting(info, "CGO_ENABLED")
		bin.Trimpath = setting(info, "-trimpath") == "true"
		bin.Deps = len(info.Deps)
		bin.OS, bin.Arch = setting(info, "GOOS"), setting(info, "GOARCH")
	}
	bin.Path = in.Exe
	if in.Exe != "" {
		if sum, err := fileSHA256(in.Exe); err == nil {
			bin.SHA256 = sum
		} else {
			b.add("binary", "checksum", Warn, "cannot read "+in.Exe+": "+err.Error())
		}
	}
	b.r.Binary = bin
	b.add("binary", "version", OK, in.Version)
	switch {
	case bin.Commit == "":
		b.add("binary", "source commit", Warn, "this binary carries no VCS stamp (built outside a git checkout, or with -buildvcs=false), so its source cannot be identified from itself; compare its sha256 with the release's SHA256SUMS")
	case bin.Modified:
		b.add("binary", "source commit", Warn, bin.Commit+" with uncommitted changes (vcs.modified=true): this is not the published source")
	default:
		b.add("binary", "source commit", OK, bin.Commit+" (clean checkout"+orEmpty(", committed "+bin.Time, bin.Time != "")+")")
	}
	build := fmt.Sprintf("%s %s/%s", bin.Go, bin.OS, bin.Arch)
	if bin.Trimpath {
		build += " · -trimpath"
	}
	if bin.CGO != "" {
		build += " · CGO_ENABLED=" + bin.CGO
	}
	b.add("binary", "build", Info, build+fmt.Sprintf(" · %d module dependencies", bin.Deps))
	if bin.SHA256 != "" {
		b.add("binary", "sha256", Info, bin.SHA256+"  "+bin.Path+" (compare with SHA256SUMS)")
	}
	if !bin.Trimpath && bin.Commit != "" {
		b.add("binary", "reproducible", Info, "not built with -trimpath, so it will not match a release binary byte for byte (make dist builds with it)")
	}
}

func orEmpty(s string, cond bool) string {
	if cond {
		return s
	}
	return ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// sandbox separates the machine's ability from the user's choice.
func (b *builder) sandbox() {
	in := b.in
	sb := in.Sandbox
	if sb == nil {
		sb = &shell.Sandbox{}
	}
	backends, auto, autoWhy := sb.Capabilities()
	s := Sandbox{Backends: backends, Auto: auto, Host: shell.HostOS()}
	profile := ""
	managed := false
	if in.Cfg != nil {
		profile = strings.TrimSpace(in.Cfg.Sandbox)
		managed = in.Cfg.Managed != nil && in.Cfg.Managed.Sandbox != ""
	}
	if profile == "off" {
		profile = ""
	}
	if (in.SandboxFlag || in.SandboxOn) && profile == "" {
		profile = "workspace"
		if in.SandboxOn {
			profile = sb.ProfileName()
		}
	}
	s.Requested, s.Profile = profile != "", profile
	supported := auto != ""
	switch {
	case s.Requested && supported:
		s.State = "enabled"
	case s.Requested:
		s.State = "requested-unavailable"
	case supported:
		s.State = "disabled-by-choice"
	default:
		s.State = "unavailable"
	}
	b.r.Sandbox = s

	names := []string{}
	for _, c := range backends {
		if c.Available {
			names = append(names, c.Name)
		}
	}
	switch s.State {
	case "enabled":
		who := "by the config file"
		switch {
		case managed:
			who = "by the managed policy"
		case in.SandboxFlag:
			who = "by --sandbox"
		}
		b.add("sandbox", "state", OK, fmt.Sprintf("on (%s profile, requested %s); %s would run it", profile, who, auto))
	case "disabled-by-choice":
		b.add("sandbox", "state", Warn, fmt.Sprintf("OFF BY CHOICE: nobody asked for it, so bash runs on the host with your permissions. This machine could run it (%s; auto picks %s): --sandbox or \"sandbox\" in the config turns it on", strings.Join(names, ", "), auto))
	case "requested-unavailable":
		b.add("sandbox", "state", Fail, fmt.Sprintf("%s profile was requested but this machine has no backend that can provide it: %s", profile, autoWhy))
	default:
		b.add("sandbox", "state", Warn, fmt.Sprintf("UNAVAILABLE ON THIS MACHINE (%s): %s. bash runs on the host; this is a limit of the machine, not a setting", s.Host, autoWhy))
	}
	for _, c := range backends {
		if c.Available {
			b.add("sandbox", "backend "+c.Name, OK, "available")
		} else {
			b.add("sandbox", "backend "+c.Name, Info, "not available: "+c.Why)
		}
	}
	b.add("sandbox", "scope", Info, "the sandbox wraps bash only; file tools always act on the workspace on the host (see the known limitations in the security manual)")
}

func (b *builder) trustStores() {
	in := b.in
	if in.TrustStore != "" {
		b.storeFile("project approvals", in.TrustStore, true)
		b.projectLevels(in.TrustStore)
	}
	if dir := filepath.Dir(in.TrustStore); in.TrustStore != "" {
		b.storeFile("extension grants", extension.StorePath(dir), true)
		b.storeFile("MCP OAuth logins", filepath.Join(dir, "mcp-auth.json"), true)
	}
	if in.CfgPath != "" {
		b.storeFile("config file", in.CfgPath, false)
	}
}

// storeFile checks a file that holds approvals or secrets: whether it is
// there, and whether anyone but the owner can write it (or, when it holds
// secrets, read it).
func (b *builder) storeFile(name, path string, secret bool) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		b.add("trust", name, Info, path+" does not exist yet")
		return
	case err != nil:
		b.add("trust", name, Fail, path+": "+err.Error())
		return
	}
	mode := info.Mode().Perm()
	switch {
	case mode&0o022 != 0:
		b.add("trust", name, Fail, fmt.Sprintf("%s is writable by others (mode %04o): anyone who can write it can approve their own files; chmod go-w", path, mode))
	case secret && mode&0o044 != 0 && name != "project approvals" && name != "extension grants":
		b.add("trust", name, Warn, fmt.Sprintf("%s holds credentials and is readable by others (mode %04o); chmod 600", path, mode))
	default:
		b.add("trust", name, OK, fmt.Sprintf("%s (mode %04o)", path, mode))
	}
}

// projectLevels counts what the approval store records, without printing
// a hash.
func (b *builder) projectLevels(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		b.add("trust", "approvals readable", Fail, path+" is not valid JSON, so every approval in it is ignored: "+err.Error())
		return
	}
	counts := map[string]int{}
	for k, v := range m {
		switch {
		case strings.HasSuffix(k, "#project"):
			counts["project level "+v]++
		case strings.Contains(k, "#"):
			counts["per-file "+k[strings.LastIndex(k, "#")+1:]]++
		default:
			counts["per-file mcp"]++
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	if len(parts) == 0 {
		parts = []string{"no approvals recorded"}
	}
	b.add("trust", "recorded approvals", Info, strings.Join(parts, " · "))
	if n := counts["project level full"]; n > 0 {
		b.add("trust", "full trust", Info, fmt.Sprintf("%s trusted at level full: their hooks, MCP servers and verify commands run without asking (/trust shows each)", textfmt.Plural(n, "project")))
	}
}

// envKeyNames are the variables a provider's key can come from without
// the config file naming one.
var envKeyNames = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENAI_API_KEY", "GEMINI_API_KEY",
	"ZAI_API_KEY", "ZAI_CODING_PLAN_API_KEY", "OPENCODE_API_KEY", "OPENCODE_ZEN_API_KEY", "OPENCODE_GO_API_KEY"}

func (b *builder) providers() {
	cfg := b.in.Cfg
	if cfg == nil {
		detail := "no configuration could be loaded"
		if b.in.CfgErr != nil {
			detail = redactText(b.in.CfgErr.Error())
		}
		b.add("providers", "configuration", Warn, detail)
		return
	}
	if b.in.CfgErr != nil && !errors.Is(b.in.CfgErr, config.ErrNoCredentials) {
		b.add("providers", "configuration", Fail, redactText(b.in.CfgErr.Error()))
	}
	var envSet []string
	for _, n := range envKeyNames {
		if strings.TrimSpace(b.in.Getenv(n)) != "" {
			envSet = append(envSet, n)
		}
	}
	for _, n := range cfg.ProviderNames() {
		p := cfg.Providers[n]
		b.provider(n, p, n == cfg.Provider, envSet)
	}
	if len(cfg.ProviderNames()) == 0 {
		b.add("providers", "providers", Info, "none configured")
	}
	if len(envSet) > 0 {
		b.add("providers", "credential variables set", Info, strings.Join(envSet, ", ")+" (names only; values are never printed)")
	}
	if cfg.CAFile != "" || b.in.Getenv("JANETCODE_CA_FILE") != "" {
		b.add("providers", "extra certificate authorities", Info, "trusted beside the system roots (ca_file / JANETCODE_CA_FILE): a TLS-intercepting proxy can read provider traffic")
	}
}

func (b *builder) provider(name string, p config.Provider, selected bool, envSet []string) {
	raw := p.BaseURL
	where := "default endpoint for its type"
	host, scheme, local := "", "https", false
	if raw != "" {
		where = extension.RedactURL(raw)
		if u, err := url.Parse(raw); err == nil {
			host, scheme = u.Hostname(), u.Scheme
			local = isLoopback(host)
		}
	}
	// Where the credential comes from.
	var src string
	keys := p.Keys()
	switch {
	case len(keys) == 0 && !p.NeedsKey():
		src = "no key needed (local server)"
	case len(keys) == 0:
		src = "no key"
	case p.KeysFromEnv() && p.KeyEnv != "":
		src = "environment variable " + p.KeyEnv
	case p.KeysFromEnv():
		src = "an environment variable (" + strings.Join(envSet, ", ") + ")"
	default:
		src = "the config file"
	}
	for _, k := range keys {
		if strings.HasPrefix(k, "op://") {
			src = "a 1Password reference resolved when a request is made"
			break
		}
	}
	detail := fmt.Sprintf("%s · %s · %s · key from %s", p.Type, where, textfmt.Plural(len(keys), "key"), src)
	if selected {
		detail += " · selected"
	}
	status := OK
	if len(keys) > 0 && scheme == "http" && !local {
		status = Fail
		detail += " · THE KEY GOES OVER PLAIN HTTP to a host that is not this machine"
	} else if scheme == "http" && !local {
		status = Warn
		detail += " · plain http to a remote host"
	}
	if len(keys) > 0 && !p.KeysFromEnv() && !strings.HasPrefix(firstKey(keys), "op://") && p.KeyEnv == "" {
		detail += " · stored in the config file in clear text; key_env or an op:// reference keeps it out"
		status = worse(status, Warn)
	}
	if b.in.CfgPath != "" && len(keys) > 0 && !p.KeysFromEnv() {
		if info, err := os.Stat(b.in.CfgPath); err == nil && info.Mode().Perm()&0o044 != 0 {
			status = worse(status, Warn)
			detail += fmt.Sprintf(" · the config file is readable by others (mode %04o)", info.Mode().Perm())
		}
	}
	b.add("providers", "provider "+name, status, detail)
}

func firstKey(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func worse(a, b string) string {
	rank := map[string]int{OK: 0, Info: 0, Warn: 1, Fail: 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func isLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1", "0.0.0.0":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func (b *builder) mcp() {
	rep := b.in.Extensions
	var user, project, remote, untrusted, restricted, disabled int
	for _, m := range rep.MCP {
		if m.Scope == extension.ScopeUser {
			user++
		} else {
			project++
		}
		if m.URL != "" {
			remote++
		}
		switch m.Trust.Status {
		case extension.StatusUntrusted:
			untrusted++
		case extension.StatusRestricted:
			restricted++
		case extension.StatusDisabled:
			disabled++
		}
	}
	b.add("mcp", "servers", Info, fmt.Sprintf("%d user-global, %d project-local, %d remote · %d untrusted (not started), %d restricted, %d disabled",
		user, project, remote, untrusted, restricted, disabled))
	for _, m := range rep.MCP {
		if m.URL != "" && strings.HasPrefix(m.URL, "http://") && !isLoopbackURL(m.URL) {
			b.add("mcp", "server "+m.Name, Warn, "remote server over plain http: "+m.URL)
		}
	}
	var hooks, hookNet int
	for _, h := range rep.Hooks {
		if h.Active {
			hooks++
			if h.Network {
				hookNet++
			}
		}
	}
	b.add("mcp", "hooks", Info, fmt.Sprintf("%d active, %d of them reach the network (your own config's hooks; a project's may not)", hooks, hookNet))
	b.add("mcp", "details", Info, "janetcode --doctor mcp lists every server, hook and skill with its trust and capabilities")
}

func isLoopbackURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && isLoopback(u.Hostname())
}

func (b *builder) privacy() {
	cfg := b.in.Cfg
	b.add("privacy", "telemetry", OK, "none: janetcode has no analytics or crash-reporting endpoint and sends no usage data anywhere")
	if cfg == nil {
		return
	}
	on := func(v bool) string {
		if v {
			return "on"
		}
		return "off"
	}
	b.add("privacy", "model requests", Info, "your prompts, file contents the model reads and tool results go to the selected provider's endpoint above, and to any fallback or routed model's provider")
	b.add("privacy", "update check", Info, on(cfg.UpdateCheckEnabled())+": asks github.com for the latest release tag at most once a day in the TUI (\"update_check\": false stops it)")
	b.add("privacy", "price table", Info, "on, not configurable: fetches LiteLLM's public price table from raw.githubusercontent.com at most daily (a plain GET; nothing about you is sent beyond the request itself)")
	hints := cfg.Hints == nil || *cfg.Hints
	b.add("privacy", "hints", Info, on(hints)+": local notes about unused features, kept in the config directory")
	b.add("privacy", "memory extraction", Info, on(cfg.Memory != nil && *cfg.Memory)+": project notes written after turns are kept locally; extracting them costs a model call to your provider")
	b.add("privacy", "proof notes", Info, on(cfg.ProofNotesEnabled())+": git notes in your own repository, never pushed by janetcode")
	if pol, err := cfg.EgressPolicy(); err == nil && pol != nil && pol.Active() {
		b.add("privacy", "egress policy", OK, pol.Describe())
	} else {
		b.add("privacy", "egress policy", Info, "none: fetch_url, web_search and package lookups may reach any host the model names (\"egress\" in the config restricts them)")
	}
	if d := cfg.Decisions; d.On() {
		keyState := "key variable " + d.KeyEnv() + " is empty, so it is off"
		if strings.TrimSpace(b.in.Getenv(d.KeyEnv())) != "" {
			keyState = "key from " + d.KeyEnv()
		}
		b.add("privacy", "decision service", Warn, fmt.Sprintf("configured (%s): turn state is sent to %s · %s", d.Model, orDefault(extension.RedactURL(d.BaseURL), "its default endpoint"), keyState))
	} else {
		b.add("privacy", "decision service", Info, "off")
	}
	b.add("privacy", "web search", Info, "backend "+orDefault(cfg.Search.Provider, "duckduckgo")+": the model's search queries go to it when web_search is used")
	b.add("privacy", "package lookups", Info, "names of packages the model installs are looked up on proxy.golang.org, registry.npmjs.org and pypi.org (under the egress policy)")
	if b.in.DataDir != "" {
		b.add("privacy", "retention", Info, "sessions, memory and usage records stay on this machine under "+b.in.DataDir+" until you delete them; there is no automatic expiry. --uninstall --purge removes them, --backup copies them")
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// redactText hides credentials in an error message that may quote a
// config value.
func redactText(s string) string { return extension.RedactText(s) }

// JSON renders the report.
func (r Report) JSON() string {
	if r.Checks == nil {
		r.Checks = []Check{}
	}
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(out)
}

// Text renders the report for a terminal or a sheet.
func (r Report) Text() string {
	var sb strings.Builder
	area := ""
	for _, c := range r.Checks {
		if c.Area != area {
			if area != "" {
				sb.WriteString("\n")
			}
			area = c.Area
			title := strings.ToUpper(area[:1]) + area[1:]
			if area == "mcp" {
				title = "MCP and hooks"
			}
			fmt.Fprintf(&sb, "%s\n", title)
		}
		mark := map[string]string{OK: "ok  ", Warn: "warn", Fail: "FAIL", Info: "info"}[c.Status]
		fmt.Fprintf(&sb, "  %s  %-26s %s\n", mark, c.Name, c.Detail)
	}
	sb.WriteString("\n")
	if r.Failed > 0 {
		fmt.Fprintf(&sb, "%s failed\n", textfmt.Plural(r.Failed, "check"))
	} else {
		sb.WriteString("no security check failed (warnings are choices and limits worth knowing, not faults)\n")
	}
	return sb.String()
}
