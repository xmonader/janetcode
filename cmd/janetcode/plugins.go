package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/plugin"
	"github.com/xmonader/janetcode/internal/plugin/plugincmd"
)

// applyPlugins folds the enabled plugins' hooks and MCP servers into the
// configuration, as though the user had written them there — but only a
// plugin whose hooks and servers are the ones the user trusted with
// /plugin trust. The others are named in cfg.Notes, which every front
// end shows. Commands, skills and agent profiles need nothing here: their
// loaders read the plugins' directories themselves.
func applyPlugins(cfgDir string, cfg *config.Config) {
	if cfg == nil || cfgDir == "" {
		return
	}
	var off []string
	defer func() {
		switch len(off) {
		case 0:
		case 1:
			cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugin %s has hooks or MCP servers you have not trusted as they are now, so they are off: /plugin show %s lists them, /plugin trust %s turns them on", off[0], off[0], off[0]))
		default:
			cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugins with hooks or MCP servers you have not trusted as they are now, so they are off: %s · /plugin show <name> lists them, /plugin trust <name> turns them on", strings.Join(off, ", ")))
		}
	}()
	for _, a := range plugin.Open(cfgDir).ActiveRuns() {
		if !a.Runs {
			off = append(off, a.Name)
			continue
		}
		if len(a.Parts.Hooks) > 0 {
			var hc hook.Config
			err := json.Unmarshal(a.Parts.Hooks, &hc)
			if err == nil {
				err = hc.Validate()
			}
			if err != nil {
				cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugin %s: hooks not loaded: %v", a.Name, err))
			} else {
				// The limits stay the user's: a plugin cannot raise the
				// ceiling on its own hooks.
				hc.Limits = hook.Limits{}
				cfg.Hooks = hook.Merge(cfg.Hooks, hc)
			}
		}
		if len(a.Parts.MCP) > 0 {
			var servers map[string]mcp.ServerConfig
			if err := json.Unmarshal(a.Parts.MCP, &servers); err != nil {
				cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugin %s: MCP servers not loaded: %v", a.Name, err))
				continue
			}
			names := make([]string, 0, len(servers))
			for n := range servers {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				sc := servers[n]
				if err := mcp.ValidServerName(n); err != nil {
					cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugin %s: MCP server %q: %v", a.Name, n, err))
					continue
				}
				if _, taken := cfg.MCPServers[n]; taken {
					cfg.Notes = append(cfg.Notes, fmt.Sprintf("plugin %s's MCP server %s has the name of one in your config, which is the one that runs", a.Name, n))
					continue
				}
				if sc.Command != "" && sc.Cwd == "" {
					sc.Cwd = a.Dir // a relative command means "in the plugin"
				}
				if cfg.MCPServers == nil {
					cfg.MCPServers = map[string]mcp.ServerConfig{}
				}
				cfg.MCPServers[n] = sc
			}
		}
	}
}

// pluginMode is --plugin: /plugin from the shell. The flag's value may
// carry the whole command ("install <url>") or only the verb, with the
// rest as positional arguments.
func pluginMode(w io.Writer, cmd string, rest []string) error {
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := append(strings.Fields(cmd), rest...)
	res, err := plugincmd.Run(ctx, filepath.Dir(cfgPath), args)
	if err != nil {
		return headless.BadInput("%v", err)
	}
	printResult(w, res)
	return nil
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
