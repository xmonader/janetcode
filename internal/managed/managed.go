// Package managed reads the administrator's managed policy: a file an
// organisation installs on a machine, outside every user's reach, whose
// settings override the user's and the project's configuration and
// cannot be changed from inside a session — not by /config, --set, a
// flag, the model, or an edit to the user's config file.
//
// It is where a company says "never --yolo on this laptop", "only these
// providers", "bash always runs sandboxed", "no web_search", "only these
// MCP servers", and "these permission rules, whatever else is saved".
//
// The file lives at a system path (see SystemPath) that only an
// administrator can write. JANETCODE_MANAGED_FILE names one more file,
// read after it, for tests and for an image that bakes a policy in; it
// can only add to the system file's policy, since the system file is
// always read.
package managed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/perm"
)

// EnvFile is the environment variable naming an extra policy file.
const EnvFile = "JANETCODE_MANAGED_FILE"

// Mode names the approval shortcuts a policy can turn off.
const (
	ModeYolo  = "yolo"  // --yolo, /yolo, auto_approve
	ModeAuto  = "auto"  // --auto, /auto, shift+tab's auto
	ModeEdits = "edits" // --auto-edits, /edits
)

// Policy is one managed policy, possibly read from two files.
type Policy struct {
	// Message is shown with every refusal the policy causes, e.g.
	// "Managed by ACME IT — see go/janetcode".
	Message string `json:"message,omitempty"`
	// Settings are config keys fixed to a value, in the config file's
	// own shape: {"effort": "high", "memory": false}. They override the
	// user's file, the environment and the command line, and /config and
	// --set refuse to change them.
	Settings map[string]json.RawMessage `json:"settings,omitempty"`
	// Permissions are rules that always apply and cannot be removed. Its
	// deny and ask rules outrank every allow rule, as any deny does.
	Permissions perm.File `json:"permissions,omitempty"`
	// ManagedRulesOnly drops every allow rule not in Permissions: the
	// user's saved rules, the project's, and --allow.
	ManagedRulesOnly bool `json:"managed_permission_rules_only,omitempty"`
	// DisabledTools are tool names (globs: mcp__*__delete*) the model is
	// never offered.
	DisabledTools []string `json:"disabled_tools,omitempty"`
	// AllowedProviders and AllowedModels, when set, are the only
	// providers (by name or type) and models (globs) a session may use.
	AllowedProviders []string `json:"allowed_providers,omitempty"`
	AllowedModels    []string `json:"allowed_models,omitempty"`
	// Sandbox forces this sandbox profile on for every session; /sandbox
	// cannot turn it off or change it.
	Sandbox string `json:"sandbox,omitempty"`
	// DisabledModes are approval shortcuts that cannot be turned on:
	// "yolo", "auto", "edits".
	DisabledModes []string `json:"disabled_modes,omitempty"`
	// AllowedMCPServers, when present, are the only MCP servers (by
	// name) that may start. An empty list allows none.
	AllowedMCPServers *[]string `json:"allowed_mcp_servers,omitempty"`

	// Sources are the files the policy was read from.
	Sources []string `json:"-"`
}

// systemPath is SystemPath, replaced in tests so a machine that has a
// real policy installed does not change what they see.
var systemPath = SystemPath

// SystemPath is where the managed policy lives on this OS.
func SystemPath() string {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("ProgramData")
		if base == "" {
			base = `C:\ProgramData`
		}
		return filepath.Join(base, "janetcode", "managed.json")
	case "darwin":
		return "/Library/Application Support/janetcode/managed.json"
	}
	return "/etc/janetcode/managed.json"
}

// Load reads the system policy file and the one EnvFile names, if any.
// No file at all is no policy: a nil *Policy, whose methods all answer
// "not managed". A file that exists and cannot be read or parsed is an
// error — a policy that silently stopped applying would be worse than a
// session that will not start.
func Load(getenv func(string) string) (*Policy, error) {
	paths := []string{systemPath()}
	if extra := strings.TrimSpace(getenv(EnvFile)); extra != "" {
		paths = append(paths, extra)
	}
	var out *Policy
	for _, p := range paths {
		pol, err := readFile(p)
		if errors.Is(err, os.ErrNotExist) {
			if p != paths[0] {
				// A named file that is missing is a mistake worth saying.
				return nil, fmt.Errorf("managed policy %s (from %s): %w", p, EnvFile, err)
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		out = merge(out, pol)
	}
	return out, nil
}

func readFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("managed policy %s: %v", path, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("managed policy %s: unexpected data after the top-level object", path)
	}
	if _, err := p.Permissions.Rules(); err != nil {
		return nil, fmt.Errorf("managed policy %s: permissions: %v", path, err)
	}
	for _, m := range p.DisabledModes {
		switch m {
		case ModeYolo, ModeAuto, ModeEdits:
		default:
			return nil, fmt.Errorf("managed policy %s: disabled_modes: unknown mode %q (want yolo, auto or edits)", path, m)
		}
	}
	p.Sources = []string{path}
	return &p, nil
}

// merge combines two policies, a the system file and b the one EnvFile
// names. b can only make the policy stricter: lists of what is refused
// are joined, lists of what is allowed are intersected, and on a single
// value — a setting, the sandbox profile, the message — a keeps its own
// and b only fills what a left unset, so a file named in the
// environment can never loosen what the administrator's file fixes.
func merge(a, b *Policy) *Policy {
	if a == nil {
		return b
	}
	out := *a
	out.Sources = append(slices.Clone(a.Sources), b.Sources...)
	if out.Message == "" {
		out.Message = b.Message
	}
	if len(b.Settings) > 0 {
		out.Settings = map[string]json.RawMessage{}
		for k, v := range b.Settings {
			out.Settings[k] = v
		}
		for k, v := range a.Settings {
			out.Settings[k] = v
		}
	}
	out.Permissions = perm.File{
		Allow: append(slices.Clone(a.Permissions.Allow), b.Permissions.Allow...),
		Deny:  append(slices.Clone(a.Permissions.Deny), b.Permissions.Deny...),
		Ask:   append(slices.Clone(a.Permissions.Ask), b.Permissions.Ask...),
	}
	out.ManagedRulesOnly = a.ManagedRulesOnly || b.ManagedRulesOnly
	out.DisabledTools = append(slices.Clone(a.DisabledTools), b.DisabledTools...)
	out.DisabledModes = append(slices.Clone(a.DisabledModes), b.DisabledModes...)
	out.AllowedProviders = intersect(a.AllowedProviders, b.AllowedProviders)
	out.AllowedModels = intersect(a.AllowedModels, b.AllowedModels)
	if out.Sandbox == "" {
		out.Sandbox = b.Sandbox
	}
	switch {
	case a.AllowedMCPServers == nil:
		out.AllowedMCPServers = b.AllowedMCPServers
	case b.AllowedMCPServers != nil:
		l := intersect(*a.AllowedMCPServers, *b.AllowedMCPServers)
		if l == nil {
			l = []string{}
		}
		out.AllowedMCPServers = &l
	}
	return &out
}

// intersect is the stricter of two allow-lists: either alone when the
// other is unset, else what both name.
func intersect(a, b []string) []string {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	out := []string{}
	for _, x := range a {
		if slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// Active reports whether there is any policy at all.
func (p *Policy) Active() bool { return p != nil }

// Refusal is the error text for something the policy forbids.
func (p *Policy) Refusal(what string) error {
	msg := what + " is not allowed by the managed policy"
	if p != nil && len(p.Sources) > 0 {
		msg += " (" + strings.Join(p.Sources, ", ") + ")"
	}
	if p != nil && p.Message != "" {
		msg += ": " + p.Message
	}
	return errors.New(msg)
}

// Locked reports whether a config key is fixed by the policy: named in
// Settings, the sandbox when the policy forces one, auto_approve when
// yolo is disabled, and a decisions.* key when the decisions block is.
func (p *Policy) Locked(key string) bool {
	if p == nil {
		return false
	}
	if _, ok := p.Settings[key]; ok {
		return true
	}
	if parent, _, ok := strings.Cut(key, "."); ok {
		if _, ok := p.Settings[parent]; ok {
			return true
		}
	}
	switch key {
	case "sandbox":
		return p.Sandbox != ""
	case "auto_approve":
		return p.ModeDisabled(ModeYolo)
	}
	return false
}

// LockedKeys are the config keys Locked reports, sorted.
func (p *Policy) LockedKeys() []string {
	if p == nil {
		return nil
	}
	var out []string
	for k := range p.Settings {
		out = append(out, k)
	}
	if p.Sandbox != "" && p.Settings["sandbox"] == nil {
		out = append(out, "sandbox")
	}
	if p.ModeDisabled(ModeYolo) && p.Settings["auto_approve"] == nil {
		out = append(out, "auto_approve")
	}
	sort.Strings(out)
	return out
}

// ModeDisabled reports whether an approval shortcut is turned off.
func (p *Policy) ModeDisabled(mode string) bool {
	return p != nil && slices.Contains(p.DisabledModes, mode)
}

// ToolDisabled reports whether a tool is withheld from the model.
func (p *Policy) ToolDisabled(name string) bool {
	if p == nil {
		return false
	}
	for _, pat := range p.DisabledTools {
		if perm.Match(pat, name) {
			return true
		}
	}
	return false
}

// ProviderAllowed reports whether a provider may be used; name is the
// configured name and typ its type (anthropic, openai), either of which
// the policy may list.
func (p *Policy) ProviderAllowed(name, typ string) bool {
	if p == nil || p.AllowedProviders == nil {
		return true
	}
	return slices.Contains(p.AllowedProviders, name) || (typ != "" && slices.Contains(p.AllowedProviders, typ))
}

// ModelAllowed reports whether a model may be used.
func (p *Policy) ModelAllowed(model string) bool {
	if p == nil || p.AllowedModels == nil {
		return true
	}
	for _, pat := range p.AllowedModels {
		if perm.Match(pat, model) {
			return true
		}
	}
	return false
}

// MCPAllowed reports whether an MCP server may start.
func (p *Policy) MCPAllowed(name string) bool {
	if p == nil || p.AllowedMCPServers == nil {
		return true
	}
	for _, pat := range *p.AllowedMCPServers {
		if perm.Match(pat, name) {
			return true
		}
	}
	return false
}

// Rules are the policy's permission rules.
func (p *Policy) Rules() perm.Rules {
	if p == nil {
		return perm.Rules{}
	}
	rs, _ := p.Permissions.Rules() // validated when read
	return rs
}

// Apply combines the policy's rules with the session's: its deny and ask
// rules are added to whatever else applies, its allow rules too, and
// under ManagedRulesOnly every allow rule not its own is dropped.
func (p *Policy) Apply(rs perm.Rules) perm.Rules {
	if p == nil {
		return rs
	}
	if p.ManagedRulesOnly {
		rs.Allow = nil
	}
	return perm.Merge(p.Rules(), rs)
}

// Summary is the policy in a few plain lines, for /permissions, /doctor
// and --inspect managed.
func (p *Policy) Summary() []string {
	if p == nil {
		return nil
	}
	var out []string
	out = append(out, "policy from "+strings.Join(p.Sources, ", "))
	if keys := p.LockedKeys(); len(keys) > 0 {
		out = append(out, "locked settings: "+strings.Join(keys, ", "))
	}
	if len(p.DisabledModes) > 0 {
		out = append(out, "disabled modes: "+strings.Join(p.DisabledModes, ", "))
	}
	if p.Sandbox != "" {
		out = append(out, "sandbox forced: "+p.Sandbox)
	}
	if len(p.DisabledTools) > 0 {
		out = append(out, "disabled tools: "+strings.Join(p.DisabledTools, ", "))
	}
	if p.AllowedProviders != nil {
		out = append(out, "allowed providers: "+orNone(p.AllowedProviders))
	}
	if p.AllowedModels != nil {
		out = append(out, "allowed models: "+orNone(p.AllowedModels))
	}
	if p.AllowedMCPServers != nil {
		out = append(out, "allowed MCP servers: "+orNone(*p.AllowedMCPServers))
	}
	if n := len(p.Permissions.Allow) + len(p.Permissions.Deny) + len(p.Permissions.Ask); n > 0 {
		s := fmt.Sprintf("permission rules: %d allow, %d deny, %d ask", len(p.Permissions.Allow), len(p.Permissions.Deny), len(p.Permissions.Ask))
		if p.ManagedRulesOnly {
			s += " (only these allow rules apply)"
		}
		out = append(out, s)
	} else if p.ManagedRulesOnly {
		out = append(out, "no allow rules apply (managed_permission_rules_only)")
	}
	if p.Message != "" {
		out = append(out, p.Message)
	}
	return out
}

func orNone(l []string) string {
	if len(l) == 0 {
		return "none"
	}
	return strings.Join(l, ", ")
}
