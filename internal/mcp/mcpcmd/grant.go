package mcpcmd

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/extension"
	"github.com/xmonader/janetcode/internal/mcp"
)

// Staged trust: /mcp add installs a server restricted, and these are the
// commands that widen or narrow it afterwards. The grants live in
// internal/extension; the manager enforces them on every call, so what
// is changed here takes effect on a session already running.

// grantKey is where the grant of name is kept: the project's when the
// project's .mcp.json declares it (that entry is the one that runs),
// otherwise the user's. cfg is the entry that governs.
func grantKey(env Env, name string) (key string, cfg mcp.ServerConfig, project bool, err error) {
	servers, _, err := mcp.LoadProject(env.Cwd)
	if err != nil {
		return "", mcp.ServerConfig{}, false, err
	}
	if c, ok := servers[name]; ok {
		return extension.ProjectKey(env.Cwd, name), c, true, nil
	}
	if c, ok := env.Global[name]; ok {
		return extension.UserKey(name), c, false, nil
	}
	return "", mcp.ServerConfig{}, false, fmt.Errorf("no MCP server called %s (/mcp lists them)", name)
}

// current is the grant in force: the recorded one, or full access for a
// server nobody restricted (the user's own config, an approved file).
func current(env Env, key string) (extension.Grant, error) {
	g, ok, err := env.Extensions.Get(key)
	if err != nil {
		return extension.Grant{}, err
	}
	if !ok {
		return extension.Full("no restriction recorded"), nil
	}
	return g, nil
}

// stage records the grant of a server just written to the project file.
// A new server, or one whose entry changed (different code to run), is
// restricted; one re-added unchanged keeps what the user already allowed.
func stage(env Env, before map[string]mcp.ServerConfig, name string, cfg mcp.ServerConfig, res *cmdresult.Result) {
	stageAs(env, before, name, cfg, false, res)
}

// stageAs is stage with the choice of how far to restrict. disabled is
// for code the user did not type: what a repository declares runs
// nothing, not even a handshake, until /mcp enable.
func stageAs(env Env, before map[string]mcp.ServerConfig, name string, cfg mcp.ServerConfig, disabled bool, res *cmdresult.Result) {
	if env.Extensions.Path == "" {
		return
	}
	key := extension.ProjectKey(env.Cwd, name)
	if prev, existed := before[name]; existed && reflect.DeepEqual(prev, cfg) {
		if _, ok, err := env.Extensions.Get(key); err == nil && ok {
			return
		}
	}
	g := extension.Restricted("installed with /mcp add: connect and read-only tools until you enable mutate")
	if disabled {
		g = extension.Grant{Scopes: []extension.Scope{}, Reason: "installed with /mcp add --from: disabled until you review what it runs and enable it", Updated: g.Updated}
	}
	if err := env.Extensions.Set(key, g); err != nil {
		res.Tips = append(res.Tips, "could not record the restricted grant for "+name+": "+err.Error()+" (it keeps whatever access it had)")
		return
	}
	if disabled {
		res.Tips = append(res.Tips, fmt.Sprintf("%s is installed disabled and was not started: read what it runs in %s, then /mcp enable %s", name, mcp.ProjectFile, name))
		return
	}
	res.Tips = append(res.Tips, fmt.Sprintf("%s is installed restricted: its read-only tools work; the rest are blocked until /mcp enable %s mutate", name, name))
}

// Enable adds capabilities to a server (all of them when scopes is
// empty) and starts it when connect is now granted.
func Enable(ctx context.Context, env Env, name string, scopes []extension.Scope) (cmdresult.Result, error) {
	key, cfg, project, err := grantKey(env, name)
	if err != nil {
		return cmdresult.Result{}, err
	}
	g, err := current(env, key)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if len(scopes) == 0 {
		scopes = extension.AllScopes
	}
	g = g.With(scopes...)
	g.Reason = "enabled by you with /mcp enable"
	if err := env.Extensions.Set(key, g); err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("%s: %s", name, describe(g))}}
	if env.Manager == nil {
		return res, nil
	}
	if _, up := env.Manager.Client(name); up {
		return res, nil
	}
	if project {
		_, hash, _ := mcp.LoadProject(env.Cwd)
		if !env.Trust.Trusted(env.Cwd, hash) {
			res.Tips = append(res.Tips, mcp.ProjectFile+" is not approved yet, so "+name+" was not started (/mcp trust)")
			return res, nil
		}
	}
	if sheet := start(ctx, env, name, cfg, &res); sheet != "" {
		res.Sheet, res.SheetTitle = sheet, name+" tools"
	}
	return res, nil
}

// Disable takes capabilities away (all of them when scopes is empty).
// Losing connect stops the server's process and removes its tools; the
// grant also refuses a start and any call made through a tool a turn
// already holds.
func Disable(env Env, name string, scopes []extension.Scope) (cmdresult.Result, error) {
	key, _, _, err := grantKey(env, name)
	if err != nil {
		return cmdresult.Result{}, err
	}
	g, err := current(env, key)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if len(scopes) == 0 {
		scopes = []extension.Scope{extension.Connect}
	}
	g = g.Without(scopes...)
	g.Reason = "disabled by you with /mcp disable"
	if err := env.Extensions.Set(key, g); err != nil {
		return cmdresult.Result{}, err
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("%s: %s", name, describe(g))}}
	if !g.Has(extension.Connect) && env.Manager != nil {
		env.Manager.Stop(name)
		res.Notes = append(res.Notes, name+" stopped · its tools are gone from this session and it will not start again until /mcp enable "+name)
	}
	return res, nil
}

// SetTool switches one tool of a server on or off by name.
func SetTool(env Env, name, tool string, on bool) (cmdresult.Result, error) {
	key, _, _, err := grantKey(env, name)
	if err != nil {
		return cmdresult.Result{}, err
	}
	if env.Manager != nil {
		if c, up := env.Manager.Client(name); up {
			found := false
			for _, t := range c.Tools() {
				if t.Name == tool {
					found = true
				}
			}
			if !found {
				return cmdresult.Result{}, fmt.Errorf("%s has no tool called %s", name, tool)
			}
		}
	}
	g, err := current(env, key)
	if err != nil {
		return cmdresult.Result{}, err
	}
	var kept []string
	for _, t := range g.DisabledTools {
		if t != tool {
			kept = append(kept, t)
		}
	}
	if !on {
		kept = append(kept, tool)
	}
	g.DisabledTools = kept
	g.Reason = "tool switched with /mcp tool"
	if err := env.Extensions.Set(key, g); err != nil {
		return cmdresult.Result{}, err
	}
	state := "off"
	if on {
		state = "on"
	}
	return cmdresult.Result{Notes: []string{fmt.Sprintf("%s: tool %s is %s", name, tool, state)}}, nil
}

// ParseScopes reads the capability words of /mcp enable and disable.
func ParseScopes(words []string) ([]extension.Scope, error) {
	var out []extension.Scope
	for _, w := range words {
		if strings.EqualFold(w, "all") {
			return nil, nil
		}
		s, err := extension.ParseScope(w)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func describe(g extension.Grant) string {
	if len(g.Scopes) == 0 {
		return "disabled"
	}
	names := make([]string, len(g.Scopes))
	for i, s := range g.Scopes {
		names[i] = string(s)
	}
	out := "allowed: " + strings.Join(names, ", ")
	if len(g.DisabledTools) > 0 {
		out += " · tools off: " + strings.Join(g.DisabledTools, ", ")
	}
	return out
}

// Usage is the text of the staged-trust verbs, for the /mcp usage line.
const Usage = "enable <name> [connect|read|mutate|all] | disable <name> [connect|read|mutate] | tool <name> <tool> on|off"

// GrantCommand runs one of the staged-trust verbs (enable, disable,
// tool) with the words that followed the server's name, so every front
// end parses them the same way.
func GrantCommand(ctx context.Context, env Env, verb, name string, rest []string) (cmdresult.Result, error) {
	if env.Extensions.Path == "" {
		return cmdresult.Result{}, fmt.Errorf("no config directory, so grants cannot be kept")
	}
	switch verb {
	case "enable", "disable":
		scopes, err := ParseScopes(rest)
		if err != nil {
			return cmdresult.Result{}, err
		}
		if verb == "enable" {
			return Enable(ctx, env, name, scopes)
		}
		return Disable(env, name, scopes)
	case "tool":
		if len(rest) != 2 || (rest[1] != "on" && rest[1] != "off") {
			return cmdresult.Result{}, fmt.Errorf("usage: /mcp tool %s <tool> on|off", name)
		}
		return SetTool(env, name, rest[0], rest[1] == "on")
	}
	return cmdresult.Result{}, fmt.Errorf("usage: /mcp %s", Usage)
}
