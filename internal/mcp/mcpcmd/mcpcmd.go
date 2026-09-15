// Package mcpcmd is what "/mcp add" and "/mcp remove" do, once a front
// end has decided what to add: write the project's .mcp.json, record that
// the user trusts what they just wrote, start the server through the same
// manager the session already uses, and say what it offers. The terminal
// dialog, the typed command and the web UI all end here, so the wording
// and the order of operations live in one place.
package mcpcmd

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/trust"
)

// Env is what these commands need from the session around them.
type Env struct {
	// Cwd is the project whose .mcp.json is edited.
	Cwd string
	// Manager owns the running servers; nil means nothing is started.
	Manager *mcptool.Manager
	// Trust records the approval of the file the command just wrote: the
	// user asked for this entry, so it does not have to be trusted again.
	Trust trust.Store
	// Global are the servers configured outside the project, so a name
	// that would be shadowed can be pointed out.
	Global map[string]mcp.ServerConfig
	// Updated tells the front end the project file changed, with the
	// servers and hash now on disk and trusted.
	Updated func(servers map[string]mcp.ServerConfig, hash string)
	// Wait bounds how long a command waits for a server to come up.
	// Zero means DefaultWait.
	Wait time.Duration
	// InstallDir is where "--from <git url>" clones a repository; the
	// servers it declares run from there. Empty disables cloning.
	InstallDir string
}

// DefaultWait is how long /mcp add waits for a new server before saying
// it is still starting.
const DefaultWait = 30 * time.Second

func (e Env) wait() time.Duration {
	if e.Wait > 0 {
		return e.Wait
	}
	return DefaultWait
}

// Add writes one server into the project's .mcp.json, trusts the result,
// starts it and reports the tools it offers.
func Add(ctx context.Context, env Env, name string, cfg mcp.ServerConfig) (cmdresult.Result, error) {
	if err := mcp.ValidServerName(name); err != nil {
		return cmdresult.Result{}, err
	}
	if cfg.Command == "" && cfg.URL == "" {
		return cmdresult.Result{}, fmt.Errorf("mcp %s: give a command to run or a URL", name)
	}
	servers, hash, err := mcp.AddProjectServer(env.Cwd, name, cfg)
	if err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("added %s to %s · %s", name, mcp.ProjectFile, mcp.Describe(cfg))}}
	applyTrust(env, servers, hash, &res)
	if _, shadowed := env.Global[name]; shadowed {
		res.Tips = append(res.Tips, fmt.Sprintf("the config file also has an MCP server called %s; its settings are the ones that run", name))
	}
	if sheet := start(ctx, env, name, cfg, &res); sheet != "" {
		res.Sheet, res.SheetTitle = sheet, name+" tools"
	}
	return res, nil
}

// Remove deletes a server from the project's .mcp.json and stops it.
func Remove(ctx context.Context, env Env, name string) (cmdresult.Result, error) {
	servers, hash, err := mcp.RemoveProjectServer(env.Cwd, name)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if env.Manager != nil {
		env.Manager.Stop(name)
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("removed %s from %s · its tools are gone from this session", name, mcp.ProjectFile)}}
	applyTrust(env, servers, hash, &res)
	if _, still := env.Global[name]; still {
		res.Tips = append(res.Tips, fmt.Sprintf("%s is also configured in the config file, where it stays", name))
	}
	return res, nil
}

// applyTrust records the new file as trusted — the user's own edit — and
// hands the front end the servers now on disk.
func applyTrust(env Env, servers map[string]mcp.ServerConfig, hash string, res *cmdresult.Result) {
	if env.Trust.Path != "" && hash != "" {
		if err := env.Trust.Trust(env.Cwd, hash); err != nil {
			res.Tips = append(res.Tips, "could not record trust for "+mcp.ProjectFile+": "+err.Error())
		}
	}
	if env.Updated != nil {
		env.Updated(servers, hash)
	}
}

// start brings a server up, folds the outcome into res and returns the
// listing of the tools it offers, which the caller places.
func start(ctx context.Context, env Env, name string, cfg mcp.ServerConfig, res *cmdresult.Result) string {
	if env.Manager == nil {
		return ""
	}
	env.Manager.Stop(name)
	done := make(chan error, 1)
	var client *mcp.Client
	if !env.Manager.Start(ctx, name, cfg, func(_ string, c *mcp.Client, err error) {
		client = c
		done <- err
	}) {
		res.Notes = append(res.Notes, "mcp "+name+" is already starting")
		return ""
	}
	select {
	case err := <-done:
		switch {
		case err != nil:
			res.Error = fmt.Sprintf("mcp %s did not start: %v", name, err)
			res.Tips = append(res.Tips, "fix the entry with /mcp add "+name+" … and it is written again, or /mcp remove "+name)
		case client == nil:
			res.Notes = append(res.Notes, "mcp "+name+" started")
		default:
			res.Notes = append(res.Notes, fmt.Sprintf("mcp %s is up · %d tools", name, len(client.Tools())))
			return toolSheet(client)
		}
	case <-time.After(env.wait()):
		res.Notes = append(res.Notes, "mcp "+name+" is still starting · /mcp lists it when it is up")
	case <-ctx.Done():
	}
	return ""
}

func toolSheet(c *mcp.Client) string {
	tools := c.Tools()
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		line := t.Name
		if d := strings.TrimSpace(strings.SplitN(t.Description, "\n", 2)[0]); d != "" {
			line += "  " + d
		}
		names = append(names, line)
	}
	sort.Strings(names)
	return strings.Join(names, "\n")
}
