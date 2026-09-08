package skill

import (
	"github.com/xmonader/janetcode/internal/md"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Profile is a custom subagent definition: a markdown file whose body is
// the subagent's extra system prompt and whose frontmatter names it and
// optionally restricts its tools or picks a model. Files live in
// .janetcode/agents/<name>.md, agents/ under the config directory, and
// .claude/agents for compatibility.
type Profile struct {
	Name        string
	Description string
	Tools       []string // empty means every read-only tool
	Model       string
	Body        string
	Path        string
	Source      string
}

// LoadProfiles reads every profile visible from cwd; a project profile
// shadows a global one of the same name.
func LoadProfiles(cwd, configDir string) []Profile {
	byName := map[string]Profile{}
	type loc struct{ dir, source string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "agents"), "global"})
	}
	if cwd != "" {
		locs = append(locs, loc{filepath.Join(cwd, ".claude", "agents"), "claude"}, loc{filepath.Join(cwd, ".janetcode", "agents"), "project"})
	}
	for _, l := range locs {
		entries, err := os.ReadDir(l.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := filepath.Join(l.dir, e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			meta, body := md.Parse(string(data))
			body = strings.TrimSpace(body)
			if body == "" {
				continue
			}
			pr := Profile{Name: strings.TrimSuffix(e.Name(), ".md"), Description: strings.TrimSpace(meta["description"]), Model: strings.TrimSpace(meta["model"]), Body: body, Path: p, Source: l.source}
			if n := strings.TrimSpace(meta["name"]); n != "" {
				pr.Name = n
			}
			if pr.Description == "" {
				pr.Description = firstLine(body)
			}
			pr.Tools = md.List(meta["tools"])
			byName[pr.Name] = pr
		}
	}
	out := make([]Profile, 0, len(byName))
	for _, p := range byName {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
