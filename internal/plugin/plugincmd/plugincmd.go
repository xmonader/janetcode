// Package plugincmd is what /plugin does, for every front end: parse the
// arguments, act on the plugin store, and say what happened as a
// cmdresult.Result, so the wording lives in one place.
package plugincmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/plugin"
)

// Usage is the command's synopsis.
const Usage = "usage: /plugin [list | show <name> | install <git url|dir|name@market> [--force] [--link] | trust <name> | enable <name> | disable <name> | update [name] | remove <name> | marketplace add <repo|dir|url> | marketplace list | marketplace update [name] | marketplace remove <name> | browse [market]]"

// Slow reports whether args name a subcommand that fetches over the
// network, which a front end runs off its event loop.
func Slow(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "install", "add", "update":
		return true
	case "marketplace", "market":
		return len(args) > 1 && (args[1] == "add" || args[1] == "update")
	}
	return false
}

// restart is what a change needs before a running session sees it.
const restart = "new sessions pick it up; restart janetcode for this one to"

// Run carries out /plugin with args against the plugins of configDir.
func Run(ctx context.Context, configDir string, args []string) (cmdresult.Result, error) {
	s := plugin.Open(configDir)
	if len(args) == 0 {
		return list(s)
	}
	verb, rest := args[0], args[1:]
	one := func() (string, error) {
		if len(rest) != 1 {
			return "", fmt.Errorf("usage: /plugin %s <name>", verb)
		}
		return rest[0], nil
	}
	switch verb {
	case "list", "ls":
		return list(s)
	case "show", "info":
		name, err := one()
		if err != nil {
			return cmdresult.Result{}, err
		}
		return show(s, name)
	case "install", "add":
		return install(ctx, s, rest)
	case "trust":
		name, err := one()
		if err != nil {
			return cmdresult.Result{}, err
		}
		parts, err := s.Trust(name)
		if err != nil {
			return cmdresult.Result{}, err
		}
		if len(parts.Hooks) == 0 && len(parts.MCP) == 0 {
			return cmdresult.Result{Notes: []string{name + " has no hooks or MCP servers; there is nothing to trust"}}, nil
		}
		return cmdresult.Result{Notes: []string{fmt.Sprintf("trusted %s's %s as they are now · %s run them", name, runsWhat(parts), restart)},
			Tips: []string{"an update that changes them turns them off again until you trust it again"}}, nil
	case "enable", "disable":
		name, err := one()
		if err != nil {
			return cmdresult.Result{}, err
		}
		if err := s.SetEnabled(name, verb == "enable"); err != nil {
			return cmdresult.Result{}, err
		}
		return cmdresult.Result{Notes: []string{fmt.Sprintf("%sd %s · %s see it", verb, name, restart)}}, nil
	case "remove", "rm", "uninstall":
		name, err := one()
		if err != nil {
			return cmdresult.Result{}, err
		}
		if err := s.Remove(name); err != nil {
			return cmdresult.Result{}, err
		}
		return cmdresult.Result{Notes: []string{fmt.Sprintf("removed %s · %s drop it", name, restart)}}, nil
	case "update", "upgrade":
		return update(ctx, s, rest)
	case "marketplace", "market":
		return market(ctx, s, rest)
	case "browse", "search":
		return browse(s, rest)
	}
	return cmdresult.Result{}, fmt.Errorf("%s", Usage)
}

func list(s plugin.Store) (cmdresult.Result, error) {
	all, err := s.List()
	if err != nil {
		return cmdresult.Result{}, err
	}
	markets, _, _ := s.Markets()
	if len(all) == 0 {
		res := cmdresult.Result{Notes: []string{"no plugins installed"}, Tips: []string{"/plugin install <git url or directory> installs one; /plugin marketplace add <repo> adds an index to browse"}}
		if len(markets) > 0 {
			res.Tips = append(res.Tips, "/plugin browse lists what the added marketplaces offer")
		}
		return res, nil
	}
	var b strings.Builder
	for _, p := range all {
		mark := "✓"
		if !p.Enabled {
			mark = "○"
		}
		line := fmt.Sprintf("%s %s", mark, p.Name)
		if p.Version != "" {
			line += " " + p.Version
		}
		if p.Description != "" {
			line += " · " + p.Description
		}
		b.WriteString(line + "\n")
		m, _, err := plugin.ReadManifest(p.Dir)
		if err != nil {
			fmt.Fprintf(&b, "    unreadable: %v\n", err)
			continue
		}
		parts := plugin.Components(p.Dir, m)
		fmt.Fprintf(&b, "    %s\n", summary(parts))
		if h := plugin.RunsHash(parts); h != "" && h != p.Trusted {
			fmt.Fprintf(&b, "    %s not trusted: /plugin show %s, then /plugin trust %s\n", runsWhat(parts), p.Name, p.Name)
		}
	}
	if len(markets) > 0 {
		names := make([]string, len(markets))
		for i, m := range markets {
			names[i] = m.Name
		}
		fmt.Fprintf(&b, "\nmarketplaces: %s · /plugin browse", strings.Join(names, ", "))
	}
	return cmdresult.Result{Sheet: strings.TrimRight(b.String(), "\n"), SheetTitle: "plugins"}, nil
}

func summary(p plugin.Parts) string {
	var bits []string
	count := func(dirs []string, what string) {
		n := 0
		for _, d := range dirs {
			n += countEntries(d)
		}
		if n > 0 {
			bits = append(bits, plural(n, what))
		}
	}
	count(p.CommandDirs, "command")
	count(p.SkillDirs, "skill")
	count(p.AgentDirs, "agent")
	if n := hookCount(p.Hooks); n > 0 {
		bits = append(bits, plural(n, "hook"))
	}
	if n := len(servers(p.MCP)); n > 0 {
		bits = append(bits, plural(n, "MCP server"))
	}
	if len(bits) == 0 {
		return "nothing janetcode reads"
	}
	return strings.Join(bits, " · ")
}

func show(s plugin.Store, name string) (cmdresult.Result, error) {
	p, ok := s.Get(name)
	if !ok {
		return cmdresult.Result{}, fmt.Errorf("no plugin named %s is installed", name)
	}
	m, file, err := plugin.ReadManifest(p.Dir)
	if err != nil {
		return cmdresult.Result{}, err
	}
	parts := plugin.Components(p.Dir, m)
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", p.Name, p.Version)
	if p.Description != "" {
		fmt.Fprintf(&b, "%s\n", p.Description)
	}
	fmt.Fprintf(&b, "source: %s\n", p.Source)
	if p.Marketplace != "" {
		fmt.Fprintf(&b, "marketplace: %s\n", p.Marketplace)
	}
	fmt.Fprintf(&b, "directory: %s", p.Dir)
	if p.Linked {
		b.WriteString(" (linked, used in place)")
	}
	b.WriteString("\n")
	if file != "" {
		fmt.Fprintf(&b, "manifest: %s\n", file)
	}
	state := "enabled"
	if !p.Enabled {
		state = "disabled"
	}
	fmt.Fprintf(&b, "state: %s\n", state)
	b.WriteString("\n" + summary(parts) + "\n")
	if len(parts.Hooks) > 0 || len(parts.MCP) > 0 {
		trusted := plugin.RunsHash(parts) == p.Trusted
		verdict := "trusted: they run"
		if !trusted {
			verdict = "NOT trusted: they do not run until /plugin trust " + p.Name
		}
		fmt.Fprintf(&b, "\nWhat it would run on this machine, with your permissions (%s):\n", verdict)
		for _, l := range hookLines(parts.Hooks) {
			fmt.Fprintf(&b, "  hook %s\n", l)
		}
		srv := servers(parts.MCP)
		names := make([]string, 0, len(srv))
		for n := range srv {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(&b, "  mcp %s: %s\n", n, srv[n])
		}
	}
	for _, pr := range parts.Problems {
		fmt.Fprintf(&b, "problem: %s\n", pr)
	}
	return cmdresult.Result{Sheet: strings.TrimRight(b.String(), "\n"), SheetTitle: "plugin " + p.Name}, nil
}

func install(ctx context.Context, s plugin.Store, args []string) (cmdresult.Result, error) {
	var o plugin.InstallOptions
	var src string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--force":
			o.Force = true
		case "--link":
			o.Link = true
		case "--name":
			if i+1 >= len(args) {
				return cmdresult.Result{}, fmt.Errorf("--name needs a value")
			}
			i++
			o.Name = args[i]
		default:
			if src != "" {
				return cmdresult.Result{}, fmt.Errorf("usage: /plugin install <git url|dir|name@market> [--force] [--link] [--name n]")
			}
			src = a
		}
	}
	rec, parts, err := s.Install(ctx, src, o)
	if err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("installed %s %s · %s · %s use it", rec.Name, rec.Version, summary(parts), restart)}}
	if len(parts.Hooks) > 0 || len(parts.MCP) > 0 {
		sheet, _ := show(s, rec.Name)
		res.Sheet, res.SheetTitle = sheet.Sheet, sheet.SheetTitle
		res.Tips = append(res.Tips, fmt.Sprintf("its %s are off until you read them above and run /plugin trust %s", runsWhat(parts), rec.Name))
	}
	for _, p := range parts.Problems {
		res.Tips = append(res.Tips, "problem: "+p)
	}
	return res, nil
}

func update(ctx context.Context, s plugin.Store, args []string) (cmdresult.Result, error) {
	var names []string
	if len(args) > 0 {
		names = args
	} else {
		all, err := s.List()
		if err != nil {
			return cmdresult.Result{}, err
		}
		for _, p := range all {
			names = append(names, p.Name)
		}
	}
	if len(names) == 0 {
		return cmdresult.Result{Notes: []string{"no plugins installed"}}, nil
	}
	var res cmdresult.Result
	for _, n := range names {
		rec, parts, changed, err := s.Update(ctx, n)
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		line := fmt.Sprintf("updated %s %s", rec.Name, rec.Version)
		if changed {
			line += fmt.Sprintf(" · its %s changed and are off until /plugin show %s, /plugin trust %s", runsWhat(parts), n, n)
		}
		res.Notes = append(res.Notes, line)
	}
	res.Tips = append(res.Tips, restart+" use the new versions")
	return res, nil
}

func market(ctx context.Context, s plugin.Store, args []string) (cmdresult.Result, error) {
	if len(args) == 0 {
		args = []string{"list"}
	}
	switch args[0] {
	case "add":
		if len(args) != 2 {
			return cmdresult.Result{}, fmt.Errorf("usage: /plugin marketplace add <git url|dir|index url>")
		}
		m, idx, err := s.AddMarket(ctx, args[1])
		if err != nil {
			return cmdresult.Result{}, err
		}
		res, _ := browse(s, []string{m.Name})
		res.Notes = append([]string{fmt.Sprintf("added marketplace %s · %s", m.Name, plural(len(idx.Plugins), "plugin"))}, res.Notes...)
		return res, nil
	case "list", "ls":
		ms, idx, err := s.Markets()
		if err != nil {
			return cmdresult.Result{}, err
		}
		if len(ms) == 0 {
			return cmdresult.Result{Notes: []string{"no marketplaces · /plugin marketplace add <repo, directory or index URL>"}}, nil
		}
		var b strings.Builder
		for _, m := range ms {
			fmt.Fprintf(&b, "%s · %s · %s\n", m.Name, plural(len(idx[m.Name].Plugins), "plugin"), m.Source)
		}
		return cmdresult.Result{Sheet: strings.TrimRight(b.String(), "\n"), SheetTitle: "marketplaces"}, nil
	case "update":
		ms, _, err := s.Markets()
		if err != nil {
			return cmdresult.Result{}, err
		}
		names := args[1:]
		if len(names) == 0 {
			for _, m := range ms {
				names = append(names, m.Name)
			}
		}
		var res cmdresult.Result
		for _, n := range names {
			idx, err := s.UpdateMarket(ctx, n)
			if err != nil {
				res.Notes = append(res.Notes, fmt.Sprintf("%s: %v", n, err))
				continue
			}
			res.Notes = append(res.Notes, fmt.Sprintf("updated marketplace %s · %s", n, plural(len(idx.Plugins), "plugin")))
		}
		return res, nil
	case "remove", "rm":
		if len(args) != 2 {
			return cmdresult.Result{}, fmt.Errorf("usage: /plugin marketplace remove <name>")
		}
		if err := s.RemoveMarket(args[1]); err != nil {
			return cmdresult.Result{}, err
		}
		return cmdresult.Result{Notes: []string{"removed marketplace " + args[1] + " · plugins installed from it stay"}}, nil
	}
	return cmdresult.Result{}, fmt.Errorf("%s", Usage)
}

func browse(s plugin.Store, args []string) (cmdresult.Result, error) {
	ms, idx, err := s.Markets()
	if err != nil {
		return cmdresult.Result{}, err
	}
	if len(ms) == 0 {
		return cmdresult.Result{Notes: []string{"no marketplaces · /plugin marketplace add <repo, directory or index URL>"}}, nil
	}
	installed := map[string]bool{}
	all, _ := s.List()
	for _, p := range all {
		installed[p.Name] = true
	}
	var b strings.Builder
	for _, m := range ms {
		if len(args) > 0 && args[0] != m.Name {
			continue
		}
		fmt.Fprintf(&b, "%s\n", m.Name)
		for _, e := range idx[m.Name].Plugins {
			line := fmt.Sprintf("  %s", e.Name)
			if e.Description != "" {
				line += " · " + e.Description
			}
			if installed[e.Name] {
				line += " (installed)"
			} else {
				line += fmt.Sprintf("\n      /plugin install %s@%s", e.Name, m.Name)
			}
			b.WriteString(line + "\n")
		}
	}
	return cmdresult.Result{Sheet: strings.TrimRight(b.String(), "\n"), SheetTitle: "available plugins"}, nil
}

func runsWhat(p plugin.Parts) string {
	switch {
	case len(p.Hooks) > 0 && len(p.MCP) > 0:
		return "hooks and MCP servers"
	case len(p.Hooks) > 0:
		return "hooks"
	}
	return "MCP servers"
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// hookLines renders each hook as "<event> [<matcher>]: <command>",
// reading both janetcode's shape and Claude Code's groups.
func hookLines(raw json.RawMessage) []string {
	var events map[string][]json.RawMessage
	if json.Unmarshal(raw, &events) != nil {
		return nil
	}
	var out []string
	for ev, list := range events {
		for _, e := range list {
			var h struct {
				Command string `json:"command"`
				Match   string `json:"match"`
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Command string `json:"command"`
				} `json:"hooks"`
			}
			if json.Unmarshal(e, &h) != nil {
				continue
			}
			m := h.Match + h.Matcher
			if m != "" {
				m = " [" + m + "]"
			}
			if h.Command != "" {
				out = append(out, fmt.Sprintf("%s%s: %s", ev, m, h.Command))
			}
			for _, g := range h.Hooks {
				out = append(out, fmt.Sprintf("%s%s: %s", ev, m, g.Command))
			}
		}
	}
	sort.Strings(out)
	return out
}

func hookCount(raw json.RawMessage) int { return len(hookLines(raw)) }

// countEntries counts what a component directory holds: markdown files,
// and directories (a skill is a directory with its SKILL.md).
func countEntries(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".md") {
			n++
		}
	}
	return n
}

// servers renders each MCP server as its command line or URL.
func servers(raw json.RawMessage) map[string]string {
	var m map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		URL     string   `json:"url"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := map[string]string{}
	for n, s := range m {
		if s.URL != "" {
			out[n] = s.URL
			continue
		}
		out[n] = strings.TrimSpace(s.Command + " " + strings.Join(s.Args, " "))
	}
	return out
}
