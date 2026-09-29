package prompt

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/plugin"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Custom is a user-defined slash command: a prompt template stored as a
// markdown file. "/name args" submits the body with $ARGUMENTS replaced.
type Custom struct {
	Name string // without the leading slash
	Help string // first heading or line of the file
	Body string
	Path string
	// Plugin names the installed plugin the command came from, if any.
	Plugin string
}

// CustomCommands loads templates from <cwd>/.janetcode/commands/*.md and
// then <configDir>/commands/*.md; a project command shadows a global one
// of the same name. Pass "" to skip a location.
func CustomCommands(cwd, configDir string) []Custom {
	byName := map[string]Custom{}
	type loc struct{ dir, plugin string }
	var locs []loc
	if configDir != "" {
		locs = append(locs, loc{filepath.Join(configDir, "commands"), ""})
		// Installed plugins' commands come after the user's own and
		// before the project's, which shadow them.
		for _, src := range plugin.Dirs(configDir, "commands") {
			locs = append(locs, loc{src.Dir, src.Plugin})
		}
	}
	if cwd != "" {
		locs = append(locs, loc{filepath.Join(cwd, ".janetcode", "commands"), ""})
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
			body := strings.TrimSpace(string(data))
			if body == "" {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			byName[name] = Custom{Name: name, Help: textfmt.HeadingLine(body, 60), Body: body, Path: p, Plugin: l.plugin}
		}
	}
	out := make([]Custom, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Expand fills the template: $ARGUMENTS gets the text after the command;
// when the template has no placeholder the arguments are appended.
func (c Custom) Expand(args string) string {
	args = strings.TrimSpace(args)
	if strings.Contains(c.Body, "$ARGUMENTS") {
		return strings.ReplaceAll(c.Body, "$ARGUMENTS", args)
	}
	if args == "" {
		return c.Body
	}
	return c.Body + "\n\n" + args
}
