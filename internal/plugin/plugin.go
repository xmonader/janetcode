// Package plugin installs and tracks plugins: a directory, usually a git
// repository, that bundles custom commands, skills, agent profiles, hooks
// and MCP servers under one name, so they are installed, updated, turned
// off and removed together.
//
// The layout is Claude Code's, read as-is, so a plugin written for it
// installs here unchanged:
//
//	.claude-plugin/plugin.json   {"name": …, "version": …, "description": …}
//	commands/*.md                custom slash commands
//	agents/*.md                  agent profiles
//	skills/<name>/SKILL.md       skills
//	hooks/hooks.json             {"hooks": {"PreToolUse": [...]}}
//	.mcp.json                    {"mcpServers": {...}}
//
// .janetcode-plugin/plugin.json is read the same way and wins when both
// exist. The manifest may point commands, agents and skills at other
// directories, and give hooks and mcpServers as a path or inline.
// ${CLAUDE_PLUGIN_ROOT} and ${JANETCODE_PLUGIN_ROOT} in a hook command or
// a server entry are the plugin's own directory.
//
// A marketplace is a JSON index of plugins, in a repository
// (.claude-plugin/marketplace.json) or at a URL; "name@market" installs
// one of its entries.
//
// This package only reads and writes files and runs git: it is imported
// by the loaders of commands, skills and profiles, which must not pull in
// the rest of the harness. Turning a plugin's hooks and servers into
// running ones is the caller's job, and only for a plugin whose current
// hooks and servers are the ones the user trusted.
package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ManifestFiles are where a plugin describes itself, in the order read.
var ManifestFiles = []string{".janetcode-plugin/plugin.json", ".claude-plugin/plugin.json"}

// Manifest is a plugin's plugin.json. Paths are relative to the plugin.
type Manifest struct {
	Name        string          `json:"name"`
	Version     string          `json:"version,omitempty"`
	Description string          `json:"description,omitempty"`
	Author      json.RawMessage `json:"author,omitempty"`
	Homepage    string          `json:"homepage,omitempty"`
	// Commands, Agents and Skills add directories to the conventional
	// ones: a path or a list of paths.
	Commands json.RawMessage `json:"commands,omitempty"`
	Agents   json.RawMessage `json:"agents,omitempty"`
	Skills   json.RawMessage `json:"skills,omitempty"`
	// Hooks and MCPServers are a path to a file or the object inline.
	Hooks      json.RawMessage `json:"hooks,omitempty"`
	MCPServers json.RawMessage `json:"mcpServers,omitempty"`
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName reports whether a plugin name can name a directory and a
// command prefix: letters, digits, dot, dash and underscore.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("plugin name %q: use letters, digits, '.', '-' and '_' (up to 64)", name)
	}
	return nil
}

// ReadManifest reads the manifest of the plugin at dir. A directory with
// no manifest is still a plugin, named after the directory, as long as it
// has something a plugin carries.
func ReadManifest(dir string) (Manifest, string, error) {
	return readManifest(dir, filepath.Base(dir))
}

// readManifest is ReadManifest with the name to use when the plugin
// declares none: a fetched copy lives in a temporary directory whose name
// means nothing.
func readManifest(dir, fallback string) (Manifest, string, error) {
	for _, f := range ManifestFiles {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Manifest{}, "", err
		}
		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return Manifest{}, "", fmt.Errorf("%s: %w", f, err)
		}
		if m.Name == "" {
			m.Name = fallback
		}
		if err := ValidName(m.Name); err != nil {
			return Manifest{}, "", fmt.Errorf("%s: %w", f, err)
		}
		return m, f, nil
	}
	m := Manifest{Name: fallback}
	c := Components(dir, m)
	if c.Empty() {
		return Manifest{}, "", fmt.Errorf("%s is not a plugin: no %s, and no commands/, agents/, skills/, hooks/hooks.json or .mcp.json", dir, strings.Join(ManifestFiles, " or "))
	}
	if err := ValidName(m.Name); err != nil {
		return Manifest{}, "", err
	}
	return m, "", nil
}

// Parts is what a plugin contributes, resolved against its directory.
type Parts struct {
	CommandDirs, AgentDirs, SkillDirs []string
	// Hooks and MCP are the raw objects ({"PreToolUse": …} and
	// {"name": {…}}), with the plugin root already substituted; empty
	// when the plugin has none.
	Hooks, MCP json.RawMessage
	// Problems are parts that could not be read.
	Problems []string
}

// Empty reports a plugin that contributes nothing.
func (p Parts) Empty() bool {
	return len(p.CommandDirs) == 0 && len(p.AgentDirs) == 0 && len(p.SkillDirs) == 0 && len(p.Hooks) == 0 && len(p.MCP) == 0
}

// Components resolves what the plugin at dir contributes.
func Components(dir string, m Manifest) Parts {
	var p Parts
	dirs := func(conventional string, extra json.RawMessage) []string {
		var out []string
		seen := map[string]bool{}
		add := func(rel string) {
			abs, ok := inside(dir, rel)
			if !ok {
				p.Problems = append(p.Problems, fmt.Sprintf("%s points outside the plugin", rel))
				return
			}
			if info, err := os.Stat(abs); err == nil && info.IsDir() && !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
		add(conventional)
		for _, rel := range pathList(extra) {
			add(rel)
		}
		return out
	}
	p.CommandDirs = dirs("commands", m.Commands)
	p.AgentDirs = dirs("agents", m.Agents)
	p.SkillDirs = dirs("skills", m.Skills)
	p.Hooks = p.object(dir, m.Hooks, "hooks/hooks.json", "hooks")
	p.MCP = p.object(dir, m.MCPServers, ".mcp.json", "mcpServers")
	return p
}

// object reads a hooks or servers object: inline in the manifest, from
// the path the manifest gives, or from the conventional file. Files and
// inline objects alike may wrap it in its key ({"hooks": {…}}).
func (p *Parts) object(dir string, spec json.RawMessage, conventional, key string) json.RawMessage {
	var raw []byte
	spec = bytes.TrimSpace(spec)
	switch {
	case len(spec) > 0 && spec[0] == '{':
		raw = spec
	default:
		rel := conventional
		if len(spec) > 0 {
			if err := json.Unmarshal(spec, &rel); err != nil {
				p.Problems = append(p.Problems, fmt.Sprintf("%s: want a path or an object", key))
				return nil
			}
		}
		abs, ok := inside(dir, rel)
		if !ok {
			p.Problems = append(p.Problems, fmt.Sprintf("%s points outside the plugin", rel))
			return nil
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) || len(spec) > 0 {
				p.Problems = append(p.Problems, fmt.Sprintf("%s: %v", rel, err))
			}
			return nil
		}
		raw = data
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		p.Problems = append(p.Problems, fmt.Sprintf("%s: %v", key, err))
		return nil
	}
	if inner, ok := wrapped[key]; ok {
		raw = inner
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil
	}
	return substituteRoot(raw, dir)
}

// substituteRoot replaces the plugin-root variables in every string of
// a JSON value, so a hook or server can name a file of its own plugin.
func substituteRoot(raw json.RawMessage, dir string) json.RawMessage {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return raw
	}
	var walk func(any) any
	walk = func(x any) any {
		switch t := x.(type) {
		case string:
			for _, k := range []string{"${CLAUDE_PLUGIN_ROOT}", "${JANETCODE_PLUGIN_ROOT}", "$CLAUDE_PLUGIN_ROOT", "$JANETCODE_PLUGIN_ROOT"} {
				t = strings.ReplaceAll(t, k, dir)
			}
			return t
		case []any:
			for i := range t {
				t[i] = walk(t[i])
			}
		case map[string]any:
			for k := range t {
				t[k] = walk(t[k])
			}
		}
		return x
	}
	out, err := json.Marshal(walk(v))
	if err != nil {
		return raw
	}
	return out
}

func pathList(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(raw, &many)
	return many
}

// inside resolves rel against dir and reports whether it stays within it.
func inside(dir, rel string) (string, bool) {
	if filepath.IsAbs(rel) {
		return "", false
	}
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	r, err := filepath.Rel(dir, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	// The check above is lexical; a link inside a cloned plugin can still
	// point out of it (to ~/.ssh, say). A path that does not exist yet has
	// nothing to follow.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		root := dir
		if rr, err := filepath.EvalSymlinks(dir); err == nil {
			root = rr
		}
		if r, err := filepath.Rel(root, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
	}
	return abs, true
}

// RunsHash identifies what a plugin would run: its hooks and servers. It
// is what the user trusts, so an update that changes a hook command asks
// again, and one that only changes a skill's wording does not.
func RunsHash(p Parts) string {
	if len(p.Hooks) == 0 && len(p.MCP) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte("hooks\x00"))
	h.Write(p.Hooks)
	h.Write([]byte("\x00mcp\x00"))
	h.Write(p.MCP)
	return hex.EncodeToString(h.Sum(nil))
}

// --- the store ---

// Installed is one installed plugin.
type Installed struct {
	Name        string `json:"-"`
	Source      string `json:"source"`
	Marketplace string `json:"marketplace,omitempty"`
	// Dir is where it lives on disk.
	Dir         string    `json:"dir"`
	Version     string    `json:"version,omitempty"`
	Description string    `json:"description,omitempty"`
	Enabled     bool      `json:"enabled"`
	Installed   time.Time `json:"installed"`
	Updated     time.Time `json:"updated,omitempty"`
	// Trusted is the RunsHash the user approved; hooks and servers run
	// only while the plugin's current hash is this one.
	Trusted string `json:"trusted,omitempty"`
	// Linked is a plugin used in place from a local directory, not a copy.
	Linked bool `json:"linked,omitempty"`
}

// Market is one known marketplace.
type Market struct {
	Name   string `json:"-"`
	Source string `json:"source"`
	// Dir holds its checkout, or the fetched index for a URL.
	Dir   string    `json:"dir"`
	Added time.Time `json:"added"`
}

type state struct {
	Plugins      map[string]*Installed `json:"plugins"`
	Marketplaces map[string]*Market    `json:"marketplaces"`
}

// Store is the plugins directory in the config directory.
type Store struct {
	// Dir is <config dir>/plugins.
	Dir string
}

// Open is the store in a config directory.
func Open(configDir string) Store { return Store{Dir: filepath.Join(configDir, "plugins")} }

func (s Store) file() string { return filepath.Join(s.Dir, "plugins.json") }

var storeMu sync.Mutex

func (s Store) load() (state, error) {
	st := state{Plugins: map[string]*Installed{}, Marketplaces: map[string]*Market{}}
	data, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("%s: %w", s.file(), err)
	}
	if st.Plugins == nil {
		st.Plugins = map[string]*Installed{}
	}
	if st.Marketplaces == nil {
		st.Marketplaces = map[string]*Market{}
	}
	for n, p := range st.Plugins {
		p.Name = n
	}
	for n, m := range st.Marketplaces {
		m.Name = n
	}
	return st, nil
}

func (s Store) save(st state) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.file() + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file())
}

// update runs f on the loaded state and saves it, under the store lock.
func (s Store) update(f func(*state) error) error {
	storeMu.Lock()
	defer storeMu.Unlock()
	st, err := s.load()
	if err != nil {
		return err
	}
	if err := f(&st); err != nil {
		return err
	}
	return s.save(st)
}

// List returns every installed plugin, sorted by name.
func (s Store) List() ([]Installed, error) {
	storeMu.Lock()
	st, err := s.load()
	storeMu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]Installed, 0, len(st.Plugins))
	for _, p := range st.Plugins {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one installed plugin.
func (s Store) Get(name string) (Installed, bool) {
	all, _ := s.List()
	for _, p := range all {
		if p.Name == name {
			return p, true
		}
	}
	return Installed{}, false
}

// Enabled returns the plugins that are on, sorted by name. A store that
// cannot be read contributes nothing rather than failing a session.
func (s Store) Enabled() []Installed {
	all, _ := s.List()
	var out []Installed
	for _, p := range all {
		if p.Enabled {
			out = append(out, p)
		}
	}
	return out
}

// Source is one directory of commands, skills or agent profiles and
// the plugin it belongs to.
type Source struct {
	Plugin string
	Dir    string
}

// Dirs lists the directories of one kind ("commands", "agents" or
// "skills") across the enabled plugins of the config directory. It is
// what the loaders call; an empty configDir means none.
func Dirs(configDir, kind string) []Source {
	if configDir == "" {
		return nil
	}
	var out []Source
	for _, p := range Open(configDir).Enabled() {
		m, _, err := ReadManifest(p.Dir)
		if err != nil {
			continue
		}
		parts := Components(p.Dir, m)
		var dirs []string
		switch kind {
		case "commands":
			dirs = parts.CommandDirs
		case "agents":
			dirs = parts.AgentDirs
		case "skills":
			dirs = parts.SkillDirs
		}
		for _, d := range dirs {
			out = append(out, Source{Plugin: p.Name, Dir: d})
		}
	}
	return out
}

// Active is an enabled plugin's hooks and servers, and whether they may
// run.
type Active struct {
	Installed
	Parts Parts
	// Hash is the current RunsHash; Runs is whether it is the trusted one.
	Hash string
	Runs bool
}

// ActiveRuns lists the enabled plugins that have hooks or servers, with
// whether each is trusted as it is now.
func (s Store) ActiveRuns() []Active {
	var out []Active
	for _, p := range s.Enabled() {
		m, _, err := ReadManifest(p.Dir)
		if err != nil {
			continue
		}
		parts := Components(p.Dir, m)
		h := RunsHash(parts)
		if h == "" {
			continue
		}
		out = append(out, Active{Installed: p, Parts: parts, Hash: h, Runs: p.Trusted == h})
	}
	return out
}
