// Package profile holds subagent profiles: the builtin ones and the
// markdown files that define custom ones. It is separate from skill so the
// agent can use profiles without the skill injector and memory store.
package profile

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/md"
	"github.com/xmonader/janetcode/internal/plugin"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Profile is a custom subagent definition: a markdown file whose body is
// the subagent's extra system prompt and whose frontmatter names it and
// optionally restricts its tools or picks a model. Files live in
// .janetcode/agents/<name>.md, .agents/agents/<name>.md or .agents/<name>.md,
// agents/ under the config directory, and .claude/agents for compatibility.
type Profile struct {
	Name        string
	Description string
	Tools       []string // empty means every read-only tool
	// Disallowed names tools the subagent may not have, applied after
	// Tools: an allowlist says what a job needs, a denylist says what no
	// job of this kind may reach, and the second has to win.
	Disallowed []string
	Model      string
	// MaxTurns caps the subagent's model calls; zero means the default
	// for its kind. MaxMinutes bounds its wall time, so a subagent that
	// will not finish cannot hold the parent's turn open.
	MaxTurns   int
	MaxMinutes int
	// Approval is how a confirmation the subagent needs is answered:
	// "bubble" queues it to the parent's approver (the user), anything
	// else leaves the subagent's own default — auto-approved inside its
	// throwaway worktree, refused outright for a read-only one.
	Approval string
	Body     string
	Path     string
	Source   string
}

// Bubble reports whether the profile sends confirmations to the user.
func (p Profile) Bubble() bool { return strings.EqualFold(p.Approval, "bubble") }

// LoadProfiles reads every profile visible from cwd; a project profile
// shadows a global one of the same name.
func LoadProfiles(cwd, configDir string) []Profile {
	byName := map[string]Profile{}
	type loc struct{ dir, source string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "agents"), "global"})
		for _, src := range plugin.Dirs(configDir, "agents") {
			locs = append(locs, loc{src.Dir, "plugin:" + src.Plugin})
		}
	}
	if cwd != "" {
		locs = append(locs,
			loc{filepath.Join(cwd, ".claude", "agents"), "claude"},
			loc{filepath.Join(cwd, ".agents"), "agents"},
			loc{filepath.Join(cwd, ".agents", "agents"), "agents"},
			loc{filepath.Join(cwd, ".janetcode", "agents"), "project"},
		)
	}
	for _, l := range locs {
		for _, pr := range readProfileDir(l.dir, l.source) {
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

// atoi reads a small positive number from frontmatter; anything else is
// treated as unset, since a budget nobody can parse is not a budget.
func atoi(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func readProfileDir(dir, source string) []Profile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") || strings.EqualFold(e.Name(), "README.md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		meta, body := md.Parse(string(data))
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		pr := Profile{Name: strings.TrimSuffix(e.Name(), ".md"), Description: strings.TrimSpace(meta["description"]), Model: strings.TrimSpace(meta["model"]), Body: body, Path: p, Source: source}
		if n := strings.TrimSpace(meta["name"]); n != "" {
			pr.Name = n
		}
		if pr.Description == "" {
			pr.Description = textfmt.HeadingLine(body, 120)
		}
		pr.Tools = md.List(meta["tools"])
		pr.Disallowed = md.List(meta["disallowed_tools"])
		pr.MaxTurns = atoi(meta["max_turns"])
		pr.MaxMinutes = atoi(meta["max_minutes"])
		pr.Approval = strings.ToLower(strings.TrimSpace(meta["approval"]))
		out = append(out, pr)
	}
	return out
}
