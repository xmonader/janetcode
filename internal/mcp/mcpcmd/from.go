package mcpcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/gitx"
	"github.com/xmonader/janetcode/internal/mcp"
)

// "/mcp add --from <git url or directory>" installs the servers a
// repository declares for itself. The contract is deliberately the
// smallest thing that already exists in the wild: a file at the
// repository's root, named .mcp.json or mcp.json, in the same shape as
// the project file —
//
//	{"mcpServers": {"name": {"command": "node", "args": ["server.js"]}}}
//
// — and nothing else. There is no registry, no manifest format of our
// own and no install script: a command entry that names a relative path
// runs from the checkout, which is why each entry gets its cwd set to the
// directory the repository was read from. Nothing is written until the
// caller confirms what Fetch found.

// ManifestFiles are the names a repository may declare its servers in,
// in the order they are looked for.
var ManifestFiles = []string{mcp.ProjectFile, "mcp.json"}

// Manifest is what a repository declares.
type Manifest struct {
	// Source is the URL or path it came from.
	Source string
	// Dir is the checkout the servers run from.
	Dir string
	// File is the manifest that was read, relative to Dir.
	File string
	// Names are the server names, sorted.
	Names []string
	// Servers are the entries, with Cwd filled in for local commands.
	Servers map[string]mcp.ServerConfig
	// Cloned reports that Dir was created by this fetch.
	Cloned bool
}

// Fetch reads the servers a repository declares. A directory is read
// where it is; anything else is cloned into env.InstallDir. It writes
// nothing into the project.
func Fetch(ctx context.Context, env Env, from string) (*Manifest, error) {
	from = strings.TrimSpace(from)
	if from == "" {
		return nil, errors.New("--from needs a git URL or a directory")
	}
	dir, cloned, err := checkout(ctx, env, from)
	if err != nil {
		return nil, err
	}
	man := &Manifest{Source: from, Dir: dir, Cloned: cloned, Servers: map[string]mcp.ServerConfig{}}
	var data []byte
	for _, name := range ManifestFiles {
		if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			data, man.File = b, name
			break
		}
	}
	if man.File == "" {
		return nil, fmt.Errorf("%s declares no MCP server: expected %s at its root, holding {\"mcpServers\": {…}}",
			from, strings.Join(ManifestFiles, " or "))
	}
	var f struct {
		Servers map[string]mcp.ServerConfig `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", from, man.File, err)
	}
	if len(f.Servers) == 0 {
		return nil, fmt.Errorf("%s/%s lists no servers under \"mcpServers\"", from, man.File)
	}
	for name, cfg := range f.Servers {
		if err := mcp.ValidServerName(name); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", from, man.File, err)
		}
		if cfg.Command != "" && cfg.Cwd == "" {
			cfg.Cwd = dir // a relative command means "in the checkout"
		}
		man.Servers[name] = cfg
		man.Names = append(man.Names, name)
	}
	sort.Strings(man.Names)
	return man, nil
}

// checkout resolves from to a directory on disk, cloning when it is not
// one already.
func checkout(ctx context.Context, env Env, from string) (dir string, cloned bool, err error) {
	if info, serr := os.Stat(from); serr == nil && info.IsDir() {
		abs, err := filepath.Abs(from)
		return abs, false, err
	}
	// git reads a leading dash as an option of its own, and some of those
	// options run a command (--upload-pack). --from takes a repository.
	if strings.HasPrefix(from, "-") {
		return "", false, fmt.Errorf("%q looks like an option, not a repository", from)
	}
	if env.InstallDir == "" {
		return "", false, fmt.Errorf("%s is not a directory, and there is nowhere to clone it to", from)
	}
	name := RepoName(from)
	if name == "" {
		return "", false, fmt.Errorf("cannot tell a repository name from %q", from)
	}
	dir = filepath.Join(env.InstallDir, name)
	if _, serr := os.Stat(filepath.Join(dir, ".git")); serr == nil {
		// Already fetched once: update it rather than clone again.
		if _, err := gitx.Run(ctx, dir, nil, "pull", "--ff-only"); err != nil {
			return dir, false, fmt.Errorf("update %s: %s", dir, gitx.Detail(err))
		}
		return dir, false, nil
	}
	if err := os.MkdirAll(env.InstallDir, 0o755); err != nil {
		return "", false, err
	}
	if _, err := gitx.Run(ctx, env.InstallDir, nil, "clone", "--depth", "1", "--", from, dir); err != nil {
		return "", false, fmt.Errorf("clone %s: %s", from, gitx.Detail(err))
	}
	return dir, true, nil
}

// RepoName is the directory a repository URL is cloned into.
func RepoName(u string) string {
	u = strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(u), "/"), ".git")
	if i := strings.LastIndexAny(u, "/:"); i >= 0 {
		u = u[i+1:]
	}
	if err := mcp.ValidServerName(u); err != nil {
		return ""
	}
	return u
}

// Preview is what --from would add, for the confirmation.
func (m *Manifest) Preview() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s declares %s in %s:\n", m.Source, plural(len(m.Names), "MCP server"), m.File)
	for _, n := range m.Names {
		fmt.Fprintf(&b, "  %s  %s\n", n, mcp.Describe(m.Servers[n]))
	}
	b.WriteString("\nThey run commands on this machine with your permissions.")
	return b.String()
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// Install writes a fetched manifest's servers into the project file,
// trusts it and starts them.
func Install(ctx context.Context, env Env, man *Manifest) (cmdresult.Result, error) {
	before, _, _ := mcp.LoadProject(env.Cwd)
	servers, hash, err := mcp.AddProjectServers(env.Cwd, man.Servers, man.Names)
	if err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("added %s from %s to %s",
		plural(len(man.Names), "server"), man.Source, mcp.ProjectFile)}}
	if man.Cloned {
		res.Notes = append(res.Notes, "cloned into "+man.Dir)
	}
	for _, name := range man.Names {
		stageAs(env, before, name, man.Servers[name], true, &res)
	}
	applyTrust(env, servers, hash, &res)
	var sheets []string
	for _, name := range man.Names {
		if env.Extensions.Path != "" {
			continue // installed disabled: nothing of it runs until /mcp enable
		}
		if sheet := start(ctx, env, name, man.Servers[name], &res); sheet != "" {
			sheets = append(sheets, name+"\n"+sheet)
		}
	}
	if len(sheets) > 0 {
		res.Sheet, res.SheetTitle = strings.Join(sheets, "\n\n"), "MCP tools"
	}
	return res, nil
}
