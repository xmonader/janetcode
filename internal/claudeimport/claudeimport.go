// Package claudeimport is /import-claude: it reads what a person already
// set up for Claude Code -- MCP servers, permission rules, skills and
// agents -- and carries it over to janetcode's own places.
//
// Nothing is written on the first call: Preview lists every item and
// whether it would be added or skipped, and ends with a plan id.
// Apply needs that id back, so what is written is what was shown; if the
// Claude files changed in between, the id differs and nothing is written.
// An entry that already exists on the janetcode side is never replaced.
// Secrets (an MCP server's env values and headers) are copied where they
// are needed to work but never printed: the preview names an env key's
// count, and a URL by its host.
//
// Project-level Claude files that janetcode already reads in place
// (.claude/skills, .claude/agents, .claude/rules, .mcp.json, CLAUDE.md)
// are not copied. Hooks, env, model and the other settings are not
// imported; the preview says so.
package claudeimport

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/trust"
)

// Env is where to read from and write to.
type Env struct {
	Home       string // the user's home; ~/.claude and ~/.claude.json are under it
	Cwd        string // the project
	ConfigPath string // janetcode's config file
	ConfigDir  string // holds skills/ and agents/
}

// Item is one thing that can be carried over.
type Item struct {
	Kind   string // "mcp", "permissions", "skills" or "agents"
	Name   string
	Detail string // shown; never a secret
	Source string // the Claude file it came from
	Dest   string // where it goes, for the preview
	// Skip is why the item will not be written: it exists already, or
	// has no janetcode equivalent. Empty means Apply writes it.
	Skip string

	fp    string // content fingerprint, for the plan id
	write func() error
}

// Plan is everything a scan found.
type Plan struct {
	Items []Item
	// Notes say what is deliberately not imported.
	Notes []string
}

// Kinds are the categories, in preview order.
var Kinds = []string{"mcp", "permissions", "skills", "agents"}

// Scan reads the Claude files. kinds limits it to some categories; none
// means all.
func Scan(env Env, kinds ...string) Plan {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	all := len(want) == 0
	var p Plan
	if all || want["mcp"] {
		p.Items = append(p.Items, scanMCP(env)...)
	}
	if all || want["permissions"] {
		p.Items = append(p.Items, scanPermissions(env)...)
	}
	if all || want["skills"] {
		p.Items = append(p.Items, scanFiles(env, "skills")...)
	}
	if all || want["agents"] {
		p.Items = append(p.Items, scanFiles(env, "agents")...)
	}
	order := map[string]int{}
	for i, k := range Kinds {
		order[k] = i
	}
	sort.SliceStable(p.Items, func(i, j int) bool { return order[p.Items[i].Kind] < order[p.Items[j].Kind] })
	p.Notes = skippedNotes(env)
	return p
}

// ID names the set of items Apply would write. It changes when any of
// them, or the Claude content behind it, does.
func (p Plan) ID() string {
	h := sha256.New()
	for _, it := range p.Items {
		if it.Skip == "" {
			fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\n", it.Kind, it.Name, it.Dest, it.Detail, it.fp)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}

// Pending counts the items Apply would write.
func (p Plan) Pending() int {
	n := 0
	for _, it := range p.Items {
		if it.Skip == "" {
			n++
		}
	}
	return n
}

// Preview renders the plan for the user.
func (p Plan) Preview() string {
	if len(p.Items) == 0 {
		return "nothing to import: no Claude Code MCP servers, permission rules, skills or agents found"
	}
	var sb strings.Builder
	kind := ""
	for _, it := range p.Items {
		if it.Kind != kind {
			kind = it.Kind
			fmt.Fprintf(&sb, "%s\n", kind)
		}
		mark := "+"
		tail := " -> " + it.Dest
		if it.Skip != "" {
			mark, tail = "-", "  (skipped: "+it.Skip+")"
		}
		fmt.Fprintf(&sb, "  %s %s", mark, it.Name)
		if it.Detail != "" {
			fmt.Fprintf(&sb, "  %s", it.Detail)
		}
		fmt.Fprintf(&sb, "%s\n", tail)
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&sb, "not imported: %s\n", n)
	}
	if n := p.Pending(); n > 0 {
		fmt.Fprintf(&sb, "\n%d item(s) would be added; existing entries are never replaced.\n/import-claude apply %s", n, p.ID())
	} else {
		sb.WriteString("\nnothing new to add.")
	}
	return sb.String()
}

// Exec is the command: "[kind...]" previews, "apply <id> [kind...]"
// writes what that preview showed.
func Exec(env Env, arg string) cmdresult.Result {
	f := strings.Fields(arg)
	apply := len(f) > 0 && f[0] == "apply"
	var id string
	if apply {
		f = f[1:]
		if len(f) > 0 && !isKind(f[0]) {
			id, f = f[0], f[1:]
		}
	}
	for _, k := range f {
		if !isKind(k) {
			return cmdresult.Result{Error: "usage: /import-claude [mcp|permissions|skills|agents ...] | apply <id> [kinds]"}
		}
	}
	plan := Scan(env, f...)
	if !apply {
		return cmdresult.Result{SheetTitle: "import from Claude Code", Sheet: plan.Preview()}
	}
	if id == "" || id != plan.ID() {
		if id == "" {
			return cmdresult.Result{Error: "apply needs the plan id the preview printed: /import-claude apply <id>"}
		}
		return cmdresult.Result{Error: "the plan changed since the preview (or the id is wrong): run /import-claude again and apply the id it prints", Sheet: plan.Preview(), SheetTitle: "import from Claude Code"}
	}
	var res cmdresult.Result
	done := 0
	for _, it := range plan.Items {
		if it.Skip != "" || it.write == nil {
			continue
		}
		if err := it.write(); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("import %s %s: %v", it.Kind, it.Name, err))
			continue
		}
		done++
	}
	res.Notes = append(res.Notes, fmt.Sprintf("imported %d of %d item(s) from Claude Code", done, plan.Pending()))
	if hasKind(plan, "mcp") {
		res.Tips = append(res.Tips, "imported MCP servers start with the next session (/mcp reconnect); a project's .mcp.json needs /mcp trust")
	}
	if hasKind(plan, "permissions") {
		res.Tips = append(res.Tips, "imported rules apply to new sessions; a project's allow rules need /permissions trust")
	}
	return res
}

func isKind(s string) bool {
	for _, k := range Kinds {
		if k == s {
			return true
		}
	}
	return false
}

func hasKind(p Plan, kind string) bool {
	for _, it := range p.Items {
		if it.Kind == kind && it.Skip == "" {
			return true
		}
	}
	return false
}

func skippedNotes(env Env) []string {
	var out []string
	check := func(path, what string) {
		if b, err := os.ReadFile(path); err == nil {
			var m map[string]json.RawMessage
			if json.Unmarshal(b, &m) == nil {
				for _, k := range []string{"hooks", "env", "model", "statusLine", "apiKeyHelper"} {
					if _, ok := m[k]; ok {
						out = append(out, k+" in "+what)
					}
				}
			}
		}
	}
	check(filepath.Join(env.Home, ".claude", "settings.json"), "~/.claude/settings.json")
	check(filepath.Join(env.Cwd, ".claude", "settings.json"), ".claude/settings.json")
	return out
}

// --- MCP -------------------------------------------------------------

func scanMCP(env Env) []Item {
	data, err := os.ReadFile(filepath.Join(env.Home, ".claude.json"))
	if err != nil {
		return nil
	}
	var f struct {
		Servers  map[string]mcp.ServerConfig `json:"mcpServers"`
		Projects map[string]struct {
			Servers map[string]mcp.ServerConfig `json:"mcpServers"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &f) != nil {
		return nil
	}
	userHave := configMCPNames(env.ConfigPath)
	projHave, _, _ := mcp.LoadProject(env.Cwd)
	var out []Item
	for _, name := range sortedKeys(f.Servers) {
		cfg := f.Servers[name]
		it := mcpItem(name, cfg, "~/.claude.json", "config mcp_servers")
		if _, ok := userHave[name]; ok {
			it.Skip = "already in the config file"
		} else {
			it.write = func() error { return addConfigMCP(env.ConfigPath, name, cfg) }
		}
		out = append(out, it)
	}
	if pr, ok := f.Projects[env.Cwd]; ok {
		for _, name := range sortedKeys(pr.Servers) {
			cfg := pr.Servers[name]
			it := mcpItem(name, cfg, "~/.claude.json (this project)", mcp.ProjectFile)
			if _, ok := projHave[name]; ok {
				it.Skip = "already in " + mcp.ProjectFile
			} else {
				it.write = func() error {
					_, _, err := mcp.AddProjectServer(env.Cwd, name, cfg)
					return err
				}
			}
			out = append(out, it)
		}
	}
	return out
}

// mcpItem describes a server without printing anything secret: the
// command's program name or the URL's host, and how many env values and
// headers ride along.
func mcpItem(name string, cfg mcp.ServerConfig, source, dest string) Item {
	var parts []string
	switch {
	case cfg.URL != "":
		host := "(url)"
		if u, err := url.Parse(cfg.URL); err == nil && u.Host != "" {
			host = u.Scheme + "://" + u.Host
		}
		parts = append(parts, host)
	case cfg.Command != "":
		parts = append(parts, filepath.Base(cfg.Command))
	}
	if n := len(cfg.Env); n > 0 {
		parts = append(parts, fmt.Sprintf("%d env value(s)", n))
	}
	if n := len(cfg.Headers); n > 0 {
		parts = append(parts, fmt.Sprintf("%d header(s)", n))
	}
	fp, _ := json.Marshal(cfg)
	sum := sha256.Sum256(fp)
	it := Item{Kind: "mcp", Name: name, Detail: strings.Join(parts, ", "), Source: source, Dest: dest, fp: hex.EncodeToString(sum[:8])}
	if cfg.URL == "" && cfg.Command == "" {
		it.Skip = "no command or url"
	}
	return it
}

func configMCPNames(path string) map[string]json.RawMessage {
	var f struct {
		Servers map[string]json.RawMessage `json:"mcp_servers"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f.Servers
}

// addConfigMCP adds one server under "mcp_servers", re-reading the file
// so a server added since the scan is not replaced.
func addConfigMCP(path, name string, cfg mcp.ServerConfig) error {
	have := configMCPNames(path)
	if _, ok := have[name]; ok {
		return fmt.Errorf("already in the config file")
	}
	servers := map[string]any{}
	for k, v := range have {
		servers[k] = v
	}
	servers[name] = cfg
	return config.UpdateKey(path, "mcp_servers", servers)
}

// --- permissions -----------------------------------------------------

type claudeSettings struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Ask   []string `json:"ask"`
	} `json:"permissions"`
}

func scanPermissions(env Env) []Item {
	type src struct{ path, label, dest string }
	srcs := []src{
		{filepath.Join(env.Home, ".claude", "settings.json"), "~/.claude/settings.json", "config"},
		{filepath.Join(env.Cwd, ".claude", "settings.json"), ".claude/settings.json", "project"},
		{filepath.Join(env.Cwd, ".claude", "settings.local.json"), ".claude/settings.local.json", "project"},
	}
	var out []Item
	seen := map[string]bool{}
	for _, s := range srcs {
		b, err := os.ReadFile(s.path)
		if err != nil {
			continue
		}
		var cs claudeSettings
		if json.Unmarshal(b, &cs) != nil {
			continue
		}
		for _, kind := range []struct {
			name string
			list []string
		}{{"deny", cs.Permissions.Deny}, {"ask", cs.Permissions.Ask}, {"allow", cs.Permissions.Allow}} {
			for _, raw := range kind.list {
				rules, why := ConvertRule(raw, env.Home)
				if len(rules) == 0 {
					out = append(out, Item{Kind: "permissions", Name: kind.name + " " + raw, Source: s.label, Skip: why})
					continue
				}
				for _, r := range rules {
					key := s.dest + "|" + kind.name + "|" + r.String()
					if seen[key] {
						continue
					}
					seen[key] = true
					out = append(out, permItem(env, s.dest, s.label, kind.name, raw, r))
				}
			}
		}
	}
	return out
}

func permItem(env Env, dest, label, kind, raw string, r perm.Rule) Item {
	it := Item{Kind: "permissions", Name: kind + " " + r.String(), Source: label, fp: raw}
	if r.Pattern == "" && kind == "allow" {
		it.Detail = "(allows every call of " + r.Tool + ")"
	}
	if dest == "config" {
		it.Dest = "config permissions"
		if has(configRules(env.ConfigPath, kind), r.String()) {
			it.Skip = "already a rule"
			return it
		}
		it.write = func() error { return config.AddPermission(env.ConfigPath, kind, r) }
		return it
	}
	it.Dest = perm.ProjectFile
	if has(projectRules(env.Cwd, kind), r.String()) {
		it.Skip = "already a rule"
		return it
	}
	it.write = func() error { return addProjectRule(env.Cwd, kind, r.String()) }
	return it
}

func has(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func pick(f perm.File, kind string) []string {
	switch kind {
	case "deny":
		return f.Deny
	case "ask":
		return f.Ask
	}
	return f.Allow
}

func configRules(path, kind string) []string {
	var c struct {
		Permissions perm.File `json:"permissions"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &c)
	}
	return pick(c.Permissions, kind)
}

func projectRules(cwd, kind string) []string {
	var f perm.File
	if b, err := os.ReadFile(filepath.Join(cwd, perm.ProjectFile)); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return pick(f, kind)
}

func addProjectRule(cwd, kind, rule string) error {
	path := filepath.Join(cwd, perm.ProjectFile)
	var raw map[string]json.RawMessage
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &raw); err != nil {
			return fmt.Errorf("%s: %w", perm.ProjectFile, err)
		}
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	var list []string
	if r, ok := raw[kind]; ok {
		_ = json.Unmarshal(r, &list)
	}
	if has(list, rule) {
		return nil
	}
	enc, _ := json.Marshal(append(list, rule))
	raw[kind] = enc
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(path, append(out, '\n'), 0o644); err != nil {
		return err
	}
	trust.SelfWrote(cwd)
	return nil
}

// ConvertRule turns one Claude permission rule ("Bash(npm test:*)") into
// janetcode rules, or says why it has none. A Claude "prefix:*" matches the
// prefix alone and anything after it, which is two janetcode patterns.
func ConvertRule(s, home string) ([]perm.Rule, string) {
	s = strings.TrimSpace(s)
	tool, pat := s, ""
	if i := strings.IndexByte(s, '('); i > 0 && strings.HasSuffix(s, ")") {
		tool, pat = s[:i], strings.TrimSpace(s[i+1:len(s)-1])
	}
	mk := func(t string, pats ...string) []perm.Rule {
		var out []perm.Rule
		for _, p := range pats {
			r, err := perm.Parse(t + paren(p))
			if err == nil {
				out = append(out, r)
			}
		}
		return out
	}
	if pat == "*" {
		pat = ""
	}
	switch tool {
	case "Bash":
		if pat == "" {
			return mk("bash", ""), ""
		}
		if p, ok := strings.CutSuffix(pat, ":*"); ok {
			return mk("bash", p, p+" *"), ""
		}
		return mk("bash", pat), ""
	case "Read":
		return mk("read_file", claudePath(pat, home)), ""
	case "Edit", "Write", "MultiEdit":
		p := claudePath(pat, home)
		return append(mk("edit_file", p), mk("write_file", p)...), ""
	case "NotebookEdit":
		return mk("notebook_edit", claudePath(pat, home)), ""
	case "Glob":
		return mk("glob", claudePath(pat, home)), ""
	case "Grep":
		return mk("grep", claudePath(pat, home)), ""
	case "WebSearch":
		return mk("web_search", ""), ""
	case "WebFetch":
		if d, ok := strings.CutPrefix(pat, "domain:"); ok && d != "" {
			return mk("fetch_url", "domain:"+d), ""
		}
		if pat == "" {
			return mk("fetch_url", ""), ""
		}
		return nil, "WebFetch rule that is not a domain"
	}
	if strings.HasPrefix(tool, "mcp__") {
		if pat == "" && strings.Count(tool, "__") >= 2 {
			return mk(tool, ""), ""
		}
		return nil, "an MCP server-wide rule has no janetcode form; name the tools"
	}
	return nil, "no janetcode tool for " + tool
}

func paren(p string) string {
	if p == "" {
		return ""
	}
	return "(" + p + ")"
}

// claudePath maps Claude's path spelling onto the cleaned paths janetcode
// matches: "//abs" is absolute, "~/x" is under home, "/x" and "./x" are
// relative to the project.
func claudePath(p, home string) string {
	switch {
	case strings.HasPrefix(p, "//"):
		return p[1:]
	case strings.HasPrefix(p, "~/"):
		return filepath.ToSlash(filepath.Join(home, p[2:]))
	case strings.HasPrefix(p, "/"):
		return strings.TrimPrefix(p, "/")
	}
	return strings.TrimPrefix(p, "./")
}

// --- skills and agents -----------------------------------------------

const (
	maxImportFiles = 64
	maxImportBytes = 256 << 10
)

// scanFiles lists ~/.claude/<kind>/ entries (skills are directories, or
// single .md files; agents are .md files) to copy into the config dir.
// Project-level ones are read in place and not listed.
func scanFiles(env Env, kind string) []Item {
	root := filepath.Join(env.Home, ".claude", kind)
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Item
	for _, e := range entries {
		src := filepath.Join(root, e.Name())
		name := strings.TrimSuffix(e.Name(), ".md")
		if e.Type()&os.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if (kind == "agents" && (e.IsDir() || !strings.HasSuffix(e.Name(), ".md"))) ||
			(kind == "skills" && !e.IsDir() && !strings.HasSuffix(e.Name(), ".md")) {
			continue
		}
		dst := filepath.Join(env.ConfigDir, kind, e.Name())
		it := Item{Kind: kind, Name: name, Source: "~/.claude/" + kind, Dest: filepath.Join("<config>", kind, e.Name())}
		files, err := listCopy(src)
		switch {
		case env.ConfigDir == "":
			it.Skip = "no config directory"
		case err != nil:
			it.Skip = err.Error()
		default:
			if _, err := os.Lstat(dst); err == nil {
				it.Skip = "already exists"
			}
			it.Detail = fmt.Sprintf("%d file(s)", len(files))
			it.fp = fileFingerprint(files)
			it.write = func() error { return copyTree(src, dst, files) }
		}
		out = append(out, it)
	}
	return out
}

// listCopy lists the regular files under src (src itself when it is a
// file), refusing a tree that is too big or holds a symlink.
func listCopy(src string) ([]string, error) {
	fi, err := os.Lstat(src)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		if fi.Size() > maxImportBytes {
			return nil, fmt.Errorf("file over %d KB", maxImportBytes>>10)
		}
		return []string{src}, nil
	}
	var files []string
	err = filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("holds a symlink")
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxImportBytes {
			return fmt.Errorf("holds a file that is not a small regular file")
		}
		if files = append(files, p); len(files) > maxImportFiles {
			return fmt.Errorf("over %d files", maxImportFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func fileFingerprint(files []string) string {
	h := sha256.New()
	for _, f := range files {
		b, _ := os.ReadFile(f)
		fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// copyTree copies the listed files under dst, creating each exclusively:
// a file that appeared since the scan is an error, not a replacement.
func copyTree(src, dst string, files []string) error {
	single := len(files) == 1 && files[0] == src
	for _, f := range files {
		to := dst
		if !single {
			rel, err := filepath.Rel(src, f)
			if err != nil {
				return err
			}
			to = filepath.Join(dst, rel)
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		w, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return err
		}
		_, werr := w.Write(data)
		if cerr := w.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
