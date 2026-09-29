package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Git runs git; tests and a machine without git replace it.
var Git = func(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// HTTPClient fetches a marketplace index given as a URL.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

// isGitURL reports a source that is fetched with git rather than read
// from disk.
func isGitURL(s string) bool {
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://", "git@"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return strings.HasSuffix(s, ".git")
}

func refuseOption(s string) error {
	// git reads a leading dash as an option of its own, and some of those
	// run a command (--upload-pack).
	if strings.HasPrefix(s, "-") {
		return fmt.Errorf("%q looks like an option, not a source", s)
	}
	return nil
}

// fetch puts source into a new directory dst: a git URL is cloned, a
// local git repository is cloned too (so update can pull), and any other
// directory is copied.
func fetch(ctx context.Context, source, dst string) error {
	if err := refuseOption(source); err != nil {
		return err
	}
	if info, err := os.Stat(source); err == nil && info.IsDir() {
		if _, err := os.Stat(filepath.Join(source, ".git")); err == nil {
			abs, _ := filepath.Abs(source)
			return Git(ctx, filepath.Dir(dst), "clone", "--depth", "1", "--", "file://"+filepath.ToSlash(abs), dst)
		}
		return copyTree(source, dst)
	}
	if !isGitURL(source) {
		return fmt.Errorf("%s is neither a directory nor a git URL", source)
	}
	return Git(ctx, filepath.Dir(dst), "clone", "--depth", "1", "--", source, dst)
}

// copyTree copies regular files and directories; links and anything else
// are left out, so a plugin cannot reach outside its own copy.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if d.Name() == ".git" && p != src {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
		return nil
	})
}

// InstallOptions tune Install.
type InstallOptions struct {
	// Name overrides the manifest's name.
	Name string
	// Force replaces a plugin already installed under the name.
	Force bool
	// Link uses a local directory in place instead of copying it, for
	// developing a plugin.
	Link bool
}

// Install fetches a plugin and records it, enabled. source is a git URL,
// a local directory, or "name@marketplace". Its hooks and servers do not
// run until Trust: the result says what they are, so the caller can show
// them first.
func (s Store) Install(ctx context.Context, source string, o InstallOptions) (Installed, Parts, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return Installed{}, Parts{}, errors.New("install what? a git URL, a directory, or name@marketplace")
	}
	market := ""
	if name, mk, ok := marketRef(source); ok {
		entry, m, err := s.findInMarket(name, mk)
		if err != nil {
			return Installed{}, Parts{}, err
		}
		src, err := entry.resolve(m)
		if err != nil {
			return Installed{}, Parts{}, err
		}
		source, market = src, m.Name
		if o.Name == "" {
			o.Name = entry.Name
		}
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "repos"), 0o755); err != nil {
		return Installed{}, Parts{}, err
	}
	var dir string
	linked := false
	if o.Link {
		abs, err := filepath.Abs(source)
		if err != nil {
			return Installed{}, Parts{}, err
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return Installed{}, Parts{}, fmt.Errorf("--link needs a local directory, not %s", source)
		}
		dir, linked = abs, true
	} else {
		tmp, err := os.MkdirTemp(filepath.Join(s.Dir, "repos"), ".fetch-")
		if err != nil {
			return Installed{}, Parts{}, err
		}
		os.Remove(tmp) // git clone wants to create it
		if err := fetch(ctx, source, tmp); err != nil {
			os.RemoveAll(tmp)
			return Installed{}, Parts{}, err
		}
		dir = tmp
	}
	fallback := o.Name
	if fallback == "" {
		fallback = strings.TrimSuffix(filepath.Base(strings.TrimRight(filepath.ToSlash(source), "/")), ".git")
	}
	m, _, err := readManifest(dir, fallback)
	if err != nil {
		if !linked {
			os.RemoveAll(dir)
		}
		return Installed{}, Parts{}, err
	}
	name := m.Name
	if o.Name != "" {
		name = o.Name
	}
	if err := ValidName(name); err != nil {
		if !linked {
			os.RemoveAll(dir)
		}
		return Installed{}, Parts{}, err
	}
	var rec Installed
	err = s.update(func(st *state) error {
		if old, ok := st.Plugins[name]; ok && !o.Force {
			return fmt.Errorf("%s is already installed (from %s): /plugin update %s fetches it again, or install with --force to replace it", name, old.Source, name)
		}
		if !linked {
			final := filepath.Join(s.Dir, "repos", name)
			if err := os.RemoveAll(final); err != nil {
				return err
			}
			if err := os.Rename(dir, final); err != nil {
				return err
			}
			dir = final
		}
		now := time.Now().UTC()
		rec = Installed{Name: name, Source: source, Marketplace: market, Dir: dir, Version: m.Version, Description: m.Description, Enabled: true, Installed: now, Linked: linked}
		st.Plugins[name] = &rec
		return nil
	})
	if err != nil {
		if !linked && strings.HasPrefix(filepath.Base(dir), ".fetch-") {
			os.RemoveAll(dir)
		}
		return Installed{}, Parts{}, err
	}
	return rec, Components(dir, m), nil
}

// Update fetches a plugin again. The trust stays only when its hooks and
// servers did not change; the returned bool says whether they did.
func (s Store) Update(ctx context.Context, name string) (Installed, Parts, bool, error) {
	p, ok := s.Get(name)
	if !ok {
		return Installed{}, Parts{}, false, fmt.Errorf("no plugin named %s is installed", name)
	}
	source := p.Source
	if p.Marketplace != "" {
		// A plugin from a marketplace follows the marketplace: its entry
		// may now point somewhere else.
		if _, err := s.UpdateMarket(ctx, p.Marketplace); err != nil {
			return Installed{}, Parts{}, false, err
		}
		if entry, m, err := s.findInMarket(name, p.Marketplace); err == nil {
			if src, err := entry.resolve(m); err == nil {
				source = src
			}
		}
	}
	if !p.Linked {
		tmp, err := os.MkdirTemp(filepath.Join(s.Dir, "repos"), ".fetch-")
		if err != nil {
			return Installed{}, Parts{}, false, err
		}
		os.Remove(tmp)
		if err := fetch(ctx, source, tmp); err != nil {
			os.RemoveAll(tmp)
			return Installed{}, Parts{}, false, err
		}
		if _, _, err := readManifest(tmp, name); err != nil {
			os.RemoveAll(tmp)
			return Installed{}, Parts{}, false, err
		}
		if err := os.RemoveAll(p.Dir); err != nil {
			os.RemoveAll(tmp)
			return Installed{}, Parts{}, false, err
		}
		if err := os.Rename(tmp, p.Dir); err != nil {
			return Installed{}, Parts{}, false, err
		}
	}
	m, _, err := ReadManifest(p.Dir)
	if err != nil {
		return Installed{}, Parts{}, false, err
	}
	parts := Components(p.Dir, m)
	h := RunsHash(parts)
	changed := h != "" && h != p.Trusted
	var rec Installed
	err = s.update(func(st *state) error {
		cur, ok := st.Plugins[name]
		if !ok {
			return fmt.Errorf("%s was removed during the update", name)
		}
		cur.Version, cur.Description, cur.Source, cur.Updated = m.Version, m.Description, source, time.Now().UTC()
		rec = *cur
		return nil
	})
	return rec, parts, changed, err
}

// Remove uninstalls a plugin. A linked one's directory is left alone.
func (s Store) Remove(name string) error {
	var dir string
	var linked bool
	err := s.update(func(st *state) error {
		p, ok := st.Plugins[name]
		if !ok {
			return fmt.Errorf("no plugin named %s is installed", name)
		}
		dir, linked = p.Dir, p.Linked
		delete(st.Plugins, name)
		return nil
	})
	if err != nil {
		return err
	}
	if !linked && strings.HasPrefix(dir, filepath.Join(s.Dir, "repos")+string(filepath.Separator)) {
		return os.RemoveAll(dir)
	}
	return nil
}

// SetEnabled turns a plugin on or off without removing it.
func (s Store) SetEnabled(name string, on bool) error {
	return s.update(func(st *state) error {
		p, ok := st.Plugins[name]
		if !ok {
			return fmt.Errorf("no plugin named %s is installed", name)
		}
		p.Enabled = on
		return nil
	})
}

// Trust records the plugin's hooks and servers, as they are now, as the
// ones the user approved.
func (s Store) Trust(name string) (Parts, error) {
	var parts Parts
	err := s.update(func(st *state) error {
		p, ok := st.Plugins[name]
		if !ok {
			return fmt.Errorf("no plugin named %s is installed", name)
		}
		m, _, err := ReadManifest(p.Dir)
		if err != nil {
			return err
		}
		parts = Components(p.Dir, m)
		p.Trusted = RunsHash(parts)
		return nil
	})
	return parts, err
}

// --- marketplaces ---

// MarketFiles are where a repository keeps its marketplace index.
var MarketFiles = []string{".janetcode-plugin/marketplace.json", ".claude-plugin/marketplace.json", "marketplace.json"}

// Index is a marketplace's list of plugins.
type Index struct {
	Name  string `json:"name"`
	Owner struct {
		Name string `json:"name"`
	} `json:"owner"`
	Metadata struct {
		PluginRoot string `json:"pluginRoot"`
	} `json:"metadata"`
	Plugins []Entry `json:"plugins"`
}

// Entry is one plugin in an index.
type Entry struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Version     string          `json:"version,omitempty"`
	Source      json.RawMessage `json:"source"`
}

// SourceText is the entry's source as one line.
func (e Entry) SourceText() string {
	var s string
	if json.Unmarshal(e.Source, &s) == nil {
		return s
	}
	var o map[string]string
	_ = json.Unmarshal(e.Source, &o)
	switch {
	case o["repo"] != "":
		return "github:" + o["repo"]
	case o["url"] != "":
		return o["url"]
	}
	return string(e.Source)
}

// resolve turns an entry's source into something fetch takes: a path in
// the marketplace's own checkout, or a git URL.
func (e Entry) resolve(m Market) (string, error) {
	var s string
	if json.Unmarshal(e.Source, &s) == nil {
		if isGitURL(s) {
			return s, nil
		}
		idx, _ := m.index()
		rel := s
		if !strings.HasPrefix(rel, "./") && !strings.HasPrefix(rel, "../") && idx.Metadata.PluginRoot != "" {
			rel = filepath.Join(idx.Metadata.PluginRoot, rel)
		}
		abs, ok := inside(m.Dir, rel)
		if !ok {
			return "", fmt.Errorf("%s's entry for %s points outside the marketplace", m.Name, e.Name)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("%s's entry for %s: %s is not in the marketplace (a marketplace added by URL can only list git sources)", m.Name, e.Name, s)
		}
		return abs, nil
	}
	var o struct {
		Source string `json:"source"`
		Repo   string `json:"repo"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(e.Source, &o); err != nil {
		return "", fmt.Errorf("%s's entry for %s has a source janetcode cannot read", m.Name, e.Name)
	}
	switch {
	case o.Repo != "" && (o.Source == "github" || o.Source == ""):
		return "https://github.com/" + strings.TrimSuffix(o.Repo, ".git") + ".git", nil
	case o.URL != "":
		return o.URL, nil
	}
	return "", fmt.Errorf("%s's entry for %s has a %q source, which janetcode cannot fetch", m.Name, e.Name, o.Source)
}

func (m Market) index() (Index, error) {
	for _, f := range append([]string{"index.json"}, MarketFiles...) {
		data, err := os.ReadFile(filepath.Join(m.Dir, f))
		if err != nil {
			continue
		}
		var idx Index
		if err := json.Unmarshal(data, &idx); err != nil {
			return Index{}, fmt.Errorf("%s: %w", f, err)
		}
		return idx, nil
	}
	return Index{}, fmt.Errorf("%s has no marketplace index (%s)", m.Source, strings.Join(MarketFiles, ", "))
}

func marketRef(s string) (name, market string, ok bool) {
	if isGitURL(s) || strings.ContainsAny(s, `/\`) {
		return "", "", false
	}
	name, market, ok = strings.Cut(s, "@")
	return name, market, ok && name != "" && market != ""
}

func (s Store) findInMarket(name, market string) (Entry, Market, error) {
	storeMu.Lock()
	st, err := s.load()
	storeMu.Unlock()
	if err != nil {
		return Entry{}, Market{}, err
	}
	m, ok := st.Marketplaces[market]
	if !ok {
		return Entry{}, Market{}, fmt.Errorf("no marketplace named %s: /plugin marketplace add <repo or URL> adds one", market)
	}
	idx, err := m.index()
	if err != nil {
		return Entry{}, Market{}, err
	}
	for _, e := range idx.Plugins {
		if e.Name == name {
			return e, *m, nil
		}
	}
	return Entry{}, Market{}, fmt.Errorf("%s lists no plugin named %s", market, name)
}

// AddMarket fetches a marketplace: a git repository or directory holding
// an index, or the URL of the index itself.
func (s Store) AddMarket(ctx context.Context, source string) (Market, Index, error) {
	source = strings.TrimSpace(source)
	if err := refuseOption(source); err != nil {
		return Market{}, Index{}, err
	}
	if err := os.MkdirAll(filepath.Join(s.Dir, "marketplaces"), 0o755); err != nil {
		return Market{}, Index{}, err
	}
	tmp, err := os.MkdirTemp(filepath.Join(s.Dir, "marketplaces"), ".fetch-")
	if err != nil {
		return Market{}, Index{}, err
	}
	if err := fetchMarket(ctx, source, tmp); err != nil {
		os.RemoveAll(tmp)
		return Market{}, Index{}, err
	}
	idx, err := Market{Source: source, Dir: tmp}.index()
	if err != nil {
		os.RemoveAll(tmp)
		return Market{}, Index{}, err
	}
	if err := ValidName(idx.Name); err != nil {
		os.RemoveAll(tmp)
		return Market{}, Index{}, fmt.Errorf("marketplace %w", err)
	}
	final := filepath.Join(s.Dir, "marketplaces", idx.Name)
	var rec Market
	err = s.update(func(st *state) error {
		if err := os.RemoveAll(final); err != nil {
			return err
		}
		if err := os.Rename(tmp, final); err != nil {
			return err
		}
		rec = Market{Name: idx.Name, Source: source, Dir: final, Added: time.Now().UTC()}
		st.Marketplaces[idx.Name] = &rec
		return nil
	})
	if err != nil {
		os.RemoveAll(tmp)
	}
	return rec, idx, err
}

func fetchMarket(ctx context.Context, source, dst string) error {
	if (strings.HasPrefix(source, "https://") || strings.HasPrefix(source, "http://")) && strings.HasSuffix(strings.SplitN(source, "?", 2)[0], ".json") {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return err
		}
		resp, err := HTTPClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", source, resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, "index.json"), data, 0o644)
	}
	os.Remove(dst)
	return fetch(ctx, source, dst)
}

// UpdateMarket fetches a marketplace's index again.
func (s Store) UpdateMarket(ctx context.Context, name string) (Index, error) {
	storeMu.Lock()
	st, err := s.load()
	storeMu.Unlock()
	if err != nil {
		return Index{}, err
	}
	m, ok := st.Marketplaces[name]
	if !ok {
		return Index{}, fmt.Errorf("no marketplace named %s", name)
	}
	_, idx, err := s.AddMarket(ctx, m.Source)
	return idx, err
}

// RemoveMarket forgets a marketplace. Plugins installed from it stay.
func (s Store) RemoveMarket(name string) error {
	var dir string
	err := s.update(func(st *state) error {
		m, ok := st.Marketplaces[name]
		if !ok {
			return fmt.Errorf("no marketplace named %s", name)
		}
		dir = m.Dir
		delete(st.Marketplaces, name)
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Markets lists the known marketplaces with their plugins.
func (s Store) Markets() ([]Market, map[string]Index, error) {
	storeMu.Lock()
	st, err := s.load()
	storeMu.Unlock()
	if err != nil {
		return nil, nil, err
	}
	var out []Market
	idx := map[string]Index{}
	for _, m := range st.Marketplaces {
		out = append(out, *m)
		if i, err := m.index(); err == nil {
			idx[m.Name] = i
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, idx, nil
}
