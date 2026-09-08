package prompt

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Custom is a user-defined slash command: a prompt template stored as a
// markdown file. "/name args" submits the body with $ARGUMENTS replaced.
type Custom struct {
	Name string // without the leading slash
	Help string // first heading or line of the file
	Body string
	Path string
}

// CustomCommands loads templates from <cwd>/.janetcode/commands/*.md and
// then <configDir>/commands/*.md; a project command shadows a global one
// of the same name. Pass "" to skip a location.
func CustomCommands(cwd, configDir string) []Custom {
	byName := map[string]Custom{}
	for _, dir := range []string{configDir, filepath.Join(cwd, ".janetcode")} {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(dir, "commands"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			p := filepath.Join(dir, "commands", e.Name())
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			body := strings.TrimSpace(string(data))
			if body == "" {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			byName[name] = Custom{Name: name, Help: firstLineOf(body), Body: body, Path: p}
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

func firstLineOf(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	line = strings.TrimSpace(strings.TrimLeft(line, "# "))
	if len(line) > 60 {
		line = line[:59] + "…"
	}
	return line
}
