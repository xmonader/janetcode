package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
)

// doctor prints what janetcode found on this machine and whether it can
// work: config, provider credentials, a live model call, directories, git
// and MCP servers. Every row is a fact with a fix hint; nothing here
// changes state.
func doctor(w io.Writer, ov config.Overrides) error {
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
		if err == nil {
			row(true, "selected", fmt.Sprintf("%s / %s", cfg.Provider, cfg.Model))
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
	cwd, _ := os.Getwd()
	if branch, ok := prompt.GitBranch(cwd); ok {
		row(true, "git", "repository on "+orDefault(branch, "detached HEAD")+" (undo snapshots and the shell cache use it)")
	} else {
		warn("git", "not a git repository: shell results are never cached here")
	}
	for _, bin := range []string{"git", "gofmt", "python3", "node", "npx", "wl-paste", "xclip"} {
		if _, err := exec.LookPath(bin); err == nil {
			fmt.Fprintf(w, "[ok  ] %-14s found\n", "tool "+bin)
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
		trust := mcp.TrustStore{Path: filepath.Join(filepath.Dir(cfgPath), "mcp-trust.json")}
		trusted := trust.Trusted(cwd, hash)
		for name, sc := range servers {
			detail := describeServer(sc)
			if !trusted {
				detail += " · not trusted yet (/mcp trust in the TUI)"
			}
			row(true, "mcp "+name, detail)
		}
	}
	fmt.Fprintln(w)
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Fprintln(w, "all checks passed")
	return nil
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
