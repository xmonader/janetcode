package webui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcpcmd"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/trust"
)

// /mcp in the browser: the listing and the non-interactive forms of add
// and remove. There is no dialog here — the terminal's step dialog is a
// terminal thing — so the page types what it wants, and anything that
// changes the project file says --yes, which is this front end's
// confirmation.

// MCPOptions are the MCP servers this session knows about. It mirrors the
// terminal's, minus the parts that only a terminal can do (OAuth in a
// browser the user is already looking at, trusting a file interactively).
type MCPOptions struct {
	Manager     *mcptool.Manager
	Global      map[string]mcp.ServerConfig
	Project     map[string]mcp.ServerConfig
	ProjectHash string
	Trust       trust.Store
}

const mcpUsage = "usage: /mcp [search <words> | add <name> <command|url> [--env K=V] [--header 'Name: value'] | add --from <repo> --yes | remove <name> --yes]"

// mcpCommand answers /mcp. Everything that writes or starts anything runs
// through runBusy, which serialises it against turns and against another
// /mcp, so the option fields below are only ever touched one at a time.
func (s *server) mcpCommand(arg string) {
	o := s.opts.MCP
	if o == nil || o.Manager == nil {
		s.errf("MCP is not configured in this session")
		return
	}
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	switch verb {
	case "":
		s.sheet("MCP servers", s.mcpListing())
	case "search":
		q := strings.TrimSpace(rest)
		s.runBusy("searching the MCP registry", func(ctx context.Context) {
			hits, err := mcpcmd.Search(ctx, nil, q)
			s.showResult(mcpcmd.SearchResult(q, hits), err)
		})
	case "add":
		// Quotes keep a header value with a space in it one argument.
		args, err := mcp.SplitArgs(rest)
		if err != nil {
			s.errf("/mcp add: %v", err)
			return
		}
		s.mcpAdd(args)
	case "remove", "rm":
		s.mcpRemove(strings.Fields(rest))
	default:
		s.errf("%s", mcpUsage)
	}
}

// mcpListing is what /mcp with no arguments prints.
func (s *server) mcpListing() string {
	o := s.opts.MCP
	var b strings.Builder
	status := o.Manager.Status()
	known := make(map[string]bool, len(status))
	for _, st := range status {
		known[st.Name] = true
		switch {
		case st.Starting:
			fmt.Fprintf(&b, "… %s starting\n", st.Name)
		case st.Err != nil:
			fmt.Fprintf(&b, "✗ %s: %v\n", st.Name, st.Err)
		default:
			fmt.Fprintf(&b, "✓ %s · %d tools\n", st.Name, len(st.Tools))
			for _, t := range st.Tools {
				fmt.Fprintf(&b, "    %s\n", t)
			}
		}
	}
	var idle []string
	for name := range o.Project {
		if !known[name] {
			idle = append(idle, name)
		}
	}
	sort.Strings(idle)
	if len(idle) > 0 {
		fmt.Fprintf(&b, "\nin %s but not running: %s\n", mcp.ProjectFile, strings.Join(idle, ", "))
	}
	if b.Len() == 0 {
		return "no MCP servers configured\n" + mcpUsage
	}
	return strings.TrimRight(b.String(), "\n")
}

// mcpEnv describes the session to the shared add/remove commands.
func (s *server) mcpEnv() mcpcmd.Env {
	o := s.opts.MCP
	env := mcpcmd.Env{
		Cwd: s.cwd(), Manager: o.Manager, Trust: o.Trust, Global: o.Global,
		Updated: func(servers map[string]mcp.ServerConfig, hash string) {
			o.Project, o.ProjectHash = servers, hash
		},
	}
	if s.opts.ConfigDir != "" {
		env.InstallDir = filepath.Join(s.opts.ConfigDir, "mcp-servers")
	}
	return env
}

func (s *server) mcpAdd(args []string) {
	if len(args) == 0 {
		s.errf("%s", mcpUsage)
		return
	}
	spec, err := mcp.ParseAdd(args)
	if err != nil {
		s.errf("%v", err)
		return
	}
	if spec.From != "" {
		s.mcpAddFrom(spec)
		return
	}
	s.runBusy("adding MCP server "+spec.Name, func(ctx context.Context) {
		s.showResult(mcpcmd.Add(ctx, s.mcpEnv(), spec.Name, spec.Config))
	})
}

// mcpAddFrom shows what a repository declares, and writes it only once
// the same command comes back with --yes.
func (s *server) mcpAddFrom(spec mcp.AddSpec) {
	s.runBusy("reading "+spec.From, func(ctx context.Context) {
		man, err := mcpcmd.Fetch(ctx, s.mcpEnv(), spec.From)
		if err != nil {
			s.errf("%v", err)
			return
		}
		if !spec.Yes {
			s.sheet("/mcp add --from "+spec.From, man.Preview())
			s.notice("nothing was written · repeat with --yes to add " + strings.Join(man.Names, ", "))
			return
		}
		s.showResult(mcpcmd.Install(ctx, s.mcpEnv(), man))
	})
}

func (s *server) mcpRemove(args []string) {
	yes := false
	var names []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			names = append(names, a)
		}
	}
	if len(names) != 1 {
		s.errf("usage: /mcp remove <name> --yes")
		return
	}
	name := names[0]
	o := s.opts.MCP
	cfg, ok := o.Project[name]
	if !ok {
		if _, global := o.Global[name]; global {
			s.errf("mcp %s comes from the config file, not %s · remove it there", name, mcp.ProjectFile)
		} else {
			s.errf("no MCP server named %q in %s", name, mcp.ProjectFile)
		}
		return
	}
	if !yes {
		s.notice(fmt.Sprintf("/mcp remove %s --yes drops %s from %s and stops it", name, mcp.Describe(cfg), mcp.ProjectFile))
		return
	}
	s.runBusy("removing MCP server "+name, func(ctx context.Context) {
		s.showResult(mcpcmd.Remove(ctx, s.mcpEnv(), name))
	})
}
