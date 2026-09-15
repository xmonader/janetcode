package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/cua"
	"github.com/xmonader/janetcode/internal/doctorfix"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/rates"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/treesitter"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/verify"
)

// doctor prints what janetcode found on this machine and whether it can
// work: config, provider credentials, a live model call, directories, git
// and MCP servers. Every row is a fact with a fix hint.
//
// With fix false nothing changes: the safe repairs are listed as "would
// fix: …" and the user decides. With fix true those repairs are carried
// out and each one printed. What counts as safe is internal/doctorfix's
// decision, and it is a narrow one — anything that needs judgement is
// reported here and never touched.
func doctor(w io.Writer, ov config.Overrides, fix bool) error {
	failed := 0
	row := func(ok bool, what, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failed++
		}
		fmt.Fprintf(w, "[%s] %-14s %s\n", mark, what, detail)
	}
	warn := func(what, detail string) { fmt.Fprintf(w, "[warn] %-14s %s\n", what, detail) }

	fmt.Fprintf(w, "janetcode %s\n\n", version)
	cfgPath, err := config.Path()
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfgPath); err == nil {
		row(true, "config", cfgPath)
	} else {
		warn("config", cfgPath+" does not exist (janetcode --init writes one; env vars work without it)")
	}
	cfg, err := config.Load(ov)
	switch {
	case err != nil && errors.Is(err, config.ErrNoCredentials):
		row(false, "credentials", err.Error())
	case err != nil:
		row(false, "config", err.Error())
	}
	if cfg != nil {
		names := cfg.ProviderNames()
		if len(names) == 0 {
			warn("providers", "none configured; set ANTHROPIC_API_KEY or OPENAI_API_KEY, or run /connect in the TUI")
		}
		for _, n := range names {
			p := cfg.Providers[n]
			keys := len(p.Keys())
			detail := fmt.Sprintf("%s · %s · %d key(s)", p.Type, orDefault(p.BaseURL, "default URL"), keys)
			if p.Model != "" {
				detail += " · last model " + p.Model
			}
			if keys == 0 && p.NeedsKey() {
				detail += " · no key"
			}
			row(keys > 0 || !p.NeedsKey(), "provider "+n, detail)
		}
		// A route that names a provider with no key resolves fine and
		// then fails at the first call, which is exactly the sort of
		// thing this command exists to catch before the call.
		for _, rt := range cfg.RoutableModels() {
			if rt.Model == cfg.Model && rt.Provider == cfg.Provider {
				continue // the selected model is reported on its own below
			}
			p, ok := cfg.Providers[rt.Provider]
			switch {
			case !ok:
				row(false, "route "+rt.Model, "routed to "+rt.Provider+", which is not configured")
			case len(p.Keys()) == 0 && p.NeedsKey():
				row(false, "route "+rt.Model, "routed to "+rt.Provider+", which has no key")
			default:
				row(true, "route "+rt.Model, "→ "+rt.Provider+" ("+rt.Why+")")
			}
		}
		if err == nil {
			row(true, "selected", fmt.Sprintf("%s / %s", cfg.Provider, cfg.Model))
			theme := cfg.Theme
			if theme == "" {
				theme = "auto"
			}
			n := 0
			if cfg.ThemeSet != nil {
				n = len(cfg.ThemeSet.Names())
			}
			row(true, "theme", fmt.Sprintf("%s (%d themes available; /theme or --inspect themes lists them)", theme, n))
			prov, perr := buildProvider(cfg)
			if perr != nil {
				row(false, "backend", perr.Error())
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				start := time.Now()
				resp, cerr := prov.Stream(ctx, llm.Request{Model: cfg.Model, MaxTokens: 16,
					Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "Reply with the single word: ok")}}, nil)
				cancel()
				if cerr != nil {
					row(false, "model call", fmt.Sprintf("%s: %v", cfg.Model, cerr))
				} else {
					row(true, "model call", fmt.Sprintf("%s answered in %s (%d in / %d out tokens)", cfg.Model, time.Since(start).Round(time.Millisecond), resp.Usage.InputTokens, resp.Usage.OutputTokens))
				}
			}
		}
	}

	// Which parser the repository map will use. Not a failure either way,
	// but it decides whether the map can tell a declaration from the same
	// words in a comment for everything that is not Go.
	if treesitter.Supported("x.py") {
		row(true, "grammars", "tree-sitter compiled in: Python, JavaScript, TypeScript, Rust and C are parsed, not matched")
	} else {
		warn("grammars", "tree-sitter not compiled in; the map reads non-Go languages with regular expressions (make build-treesitter)")
	}

	sessDir, err := session.Dir()
	if err != nil {
		row(false, "sessions dir", err.Error())
	} else if werr := writable(sessDir); werr != nil {
		row(false, "sessions dir", sessDir+": "+werr.Error())
	} else {
		cwd, _ := os.Getwd()
		n := 0
		if infos, lerr := session.List(sessDir, cwd); lerr == nil {
			n = len(infos)
		}
		row(true, "sessions dir", fmt.Sprintf("%s (%d for this directory)", sessDir, n))
	}
	if p, perr := config.Path(); perr == nil {
		dir := filepath.Dir(p)
		if t := rates.Load(dir); t != nil {
			row(true, "prices", fmt.Sprintf("%d models from LiteLLM's rate table, fetched %s ago (config \"prices\" entries win)", len(t.Models), textfmt.Age(time.Since(t.FetchedAt))))
		} else {
			row(true, "prices", "no rate table cached yet; it is fetched in the background on the first run with network (config \"prices\" entries always work)")
		}
	}
	cwd, _ := os.Getwd()
	if branch, ok := prompt.GitBranch(cwd); ok {
		row(true, "git", "repository on "+orDefault(branch, "detached HEAD")+" (undo snapshots and the shell cache use it)")
	} else {
		warn("git", "not a git repository: shell results are never cached here")
	}
	for _, bin := range []string{"git", "gofmt", "python3", "node", "npx", "wl-paste", "xclip", "cua-driver", "open-computer-use"} {
		if _, err := exec.LookPath(bin); err == nil {
			fmt.Fprintf(w, "[ok  ] %-14s found\n", "tool "+bin)
		}
	}
	if cfg != nil && cfg.ComputerUseEnabled() {
		if d, ok := cua.Detect(); ok {
			row(true, "cua", d.Kind+" at "+d.Path+" · /cua doctor")
		} else {
			warn("cua", "computer_use is on but no driver is on PATH")
		}
	}
	if cfg != nil {
		for name, sc := range cfg.MCPServers {
			row(true, "mcp "+name, describeServer(sc))
		}
	}
	if servers, hash, perr := mcp.LoadProject(cwd); perr != nil {
		row(false, "mcp project", perr.Error())
	} else if len(servers) > 0 {
		ts := trust.Store{Path: filepath.Join(filepath.Dir(cfgPath), "mcp-trust.json")}
		trusted := ts.Trusted(cwd, hash)
		for name, sc := range servers {
			detail := describeServer(sc)
			if !trusted {
				detail += " · not trusted yet (/mcp trust in the TUI)"
			}
			row(true, "mcp "+name, detail)
		}
	}
	ts := trust.Store{Path: filepath.Join(filepath.Dir(cfgPath), "mcp-trust.json")}
	doctorProject(w, row, warn, cfg, cwd, filepath.Dir(cfgPath), sessDir, ts)
	fmt.Fprintln(w)
	printResult(w, doctorfix.Run(fixEnv(cfgPath, cwd, sessDir, ts), fix))
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Fprintln(w, "all checks passed")
	return nil
}

// fixEnv points the repair pass at this machine's files: the config
// directory, the project, and the directories a feature stops working
// without.
func fixEnv(cfgPath, cwd, sessDir string, ts trust.Store) doctorfix.Env {
	cfgDir := filepath.Dir(cfgPath)
	dirs := []string{filepath.Join(cfgDir, "skills"), filepath.Join(cwd, ".janetcode", "skills")}
	if sessDir != "" {
		dirs = append(dirs, memory.Open(filepath.Dir(sessDir), cwd).Root)
	}
	return doctorfix.Env{ConfigPath: cfgPath, Cwd: cwd, Trust: ts, Dirs: dirs,
		Tokens: &mcp.TokenStore{Path: filepath.Join(cfgDir, "mcp-auth.json")}}
}

// printResult renders what a core command returned for a terminal that
// is not the TUI: the sheet, then the error, the notes and the tips.
func printResult(w io.Writer, res cmdresult.Result) {
	if res.Sheet != "" {
		fmt.Fprintln(w, res.Sheet)
	}
	if res.Error != "" {
		fmt.Fprintln(w, res.Error)
	}
	for _, n := range res.Notes {
		fmt.Fprintln(w, n)
	}
	for _, t := range res.Tips {
		fmt.Fprintln(w, t)
	}
}

func describeServer(sc mcp.ServerConfig) string {
	if sc.URL != "" {
		return "remote " + sc.URL
	}
	if _, err := exec.LookPath(sc.Command); err != nil {
		return "command " + sc.Command + " NOT FOUND in PATH"
	}
	return "command " + sc.Command
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// writable creates dir if needed and checks a file can be written there.
func writable(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// doctorProject reports the per-project extras: hooks, verification
// commands, permission rules, skills, memory and language servers. Rows
// fail only for broken files; a missing feature is a note.
func doctorProject(w io.Writer, row func(bool, string, string), warn func(string, string), cfg *config.Config, cwd, cfgDir, sessDir string, ts trust.Store) {
	plural := func(n int, what string) string {
		if n == 1 {
			return "1 " + what
		}
		return fmt.Sprintf("%d %s", n, strings.TrimSuffix(what, "y")+map[bool]string{true: "ies", false: "s"}[strings.HasSuffix(what, "y")])
	}
	trustNote := func(trusted bool, cmd string) string {
		if trusted {
			return "trusted"
		}
		return "not trusted yet (" + cmd + " in the TUI)"
	}

	global := 0
	if cfg != nil {
		global = cfg.Hooks.Len()
	}
	if hooks, hash, err := hook.LoadProject(cwd); err != nil {
		row(false, "hooks", err.Error())
	} else {
		n := hooks.Len()
		switch {
		case n > 0:
			row(true, "hooks", fmt.Sprintf("%s in %s · %s · %d global", plural(n, "hook"), hook.ProjectFile, trustNote(ts.Trusted(trust.HooksKey(cwd), hash), "/hooks trust"), global))
		case global > 0:
			row(true, "hooks", fmt.Sprintf("%d global, none in %s", global, hook.ProjectFile))
		default:
			warn("hooks", "none configured (config \"hooks\" or "+hook.ProjectFile+")")
		}
	}

	if vc, hash, err := verify.Load(cwd); err != nil {
		row(false, "verify", err.Error())
	} else if len(vc.Commands) == 0 {
		warn("verify", "no verification commands ("+verify.ProjectFile+"; /verify set <cmd> adds one)")
	} else {
		row(true, "verify", fmt.Sprintf("%s in %s · %s", plural(len(vc.Commands), "command"), verify.ProjectFile, trustNote(ts.Trusted(trust.VerifyKey(cwd), hash), "/verify trust")))
	}

	if rules, hash, err := perm.LoadProject(cwd); err != nil {
		row(false, "permissions", err.Error())
	} else if len(rules.Allow)+len(rules.Deny) == 0 {
		n := 0
		if cfg != nil {
			n = len(cfg.Permissions.Allow) + len(cfg.Permissions.Deny)
		}
		if n > 0 {
			row(true, "permissions", fmt.Sprintf("%s in the config, none in %s", plural(n, "rule"), perm.ProjectFile))
		} else {
			warn("permissions", "no rules; every mutating tool asks (or use --yolo)")
		}
	} else {
		detail := fmt.Sprintf("%d allow / %d deny in %s", len(rules.Allow), len(rules.Deny), perm.ProjectFile)
		if len(rules.Allow) > 0 {
			detail += " · allow rules " + trustNote(ts.Trusted(trust.PermissionsKey(cwd), hash), "/permissions trust")
		}
		row(true, "permissions", detail)
	}

	skills := skill.Load(cwd, cfgDir)
	if len(skills) == 0 {
		warn("skills", "none found (.janetcode/skills, "+filepath.Join(cfgDir, "skills")+", .claude/skills)")
	} else {
		by := map[string]int{}
		for _, sk := range skills {
			by[sk.Source]++
		}
		row(true, "skills", fmt.Sprintf("%s (%d project, %d global, %d claude)", plural(len(skills), "skill"), by["project"], by["global"], by["claude"]))
	}
	if profiles := skill.LoadProfiles(cwd, cfgDir); len(profiles) > 0 {
		row(true, "profiles", plural(len(profiles), "subagent profile"))
	}

	mem := memory.Open(filepath.Dir(sessDir), cwd)
	if err := writable(mem.Root); err != nil {
		row(false, "memory", mem.Root+": "+err.Error())
	} else {
		entries := mem.Entries()
		scope := map[string]int{}
		for _, e := range entries {
			scope[e.Scope]++
		}
		detail := fmt.Sprintf("%s (%d project, %d global) in %s", plural(len(entries), "memory entry"), scope["project"], scope["global"], mem.Root)
		if cfg != nil && cfg.EmbeddingModel != "" {
			detail += " · semantic recall with " + cfg.EmbeddingModel
		} else {
			detail += " · lexical recall only (set embedding_model for semantic)"
		}
		row(true, "memory", detail)
	}

	var lc lsp.Config
	if cfg != nil {
		lc = cfg.LSP
	}
	if lc.Disabled {
		warn("lsp", "diagnostics disabled in the config")
		return
	}
	active := lc.Active()
	names := make([]string, 0, len(active))
	for n := range active {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		srv := active[n]
		if len(srv.Command) == 0 {
			continue
		}
		if path, err := exec.LookPath(srv.Command[0]); err == nil {
			row(true, "lsp "+n, path+" · "+strings.Join(srv.Extensions, " "))
		} else {
			warn("lsp "+n, srv.Command[0]+" not on PATH; "+strings.Join(srv.Extensions, " ")+" edits get no diagnostics")
		}
	}
}
