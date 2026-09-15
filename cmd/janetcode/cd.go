package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/lsp"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/repomap"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/snapshot"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/verify"
)

// rootedDeps is what /cd has to move besides the workspace, the agent's
// own Root and the session metadata, which agent.CD moves itself.
type rootedDeps struct {
	d       agentDeps
	ag      *agent.Agent
	task    *agent.Task
	script  *agent.Script
	diag    *lsp.Manager
	repo    *repomap.Cache
	mem     *memory.Store
	terms   *tool.Terms
	jobs    *tool.Jobs
	running *tool.Running
	skills  func() []skill.Skill
	rules   func() []rules.Rule
}

// registerRooted tells the /cd tracker about every part of a session that
// is rooted at the project directory. Three kinds of thing end up here:
//
//   - things that simply move (the repository map, memory, the system
//     prompt's environment section, the subagent checkout root);
//   - things that are looked up lazily from cd.Dir() and so are already
//     correct — registered anyway, so the move can report what the new
//     directory turned out to have, and so a test can walk the whole list;
//   - things that cannot move and are reported instead (a language server
//     already indexing the old tree is stopped, a command already running
//     there keeps running, the session's tool-output archive stays where
//     this conversation's results were written).
//
// The registered order is the order the report reads in.
func registerRooted(cd *agent.CD, r rootedDeps) {
	cd.Add("system prompt", func(dir string) (string, error) {
		// The environment section names the working directory, the git
		// branch and the project's own instruction files, and it is built
		// once. Rebuilding it is what actually tells the model where it is;
		// it costs no cached prefix, because the provider's cache
		// breakpoint sits above the "# Environment" heading.
		r.ag.SetSystem(prompt.SystemWith(dir, r.d.promptOverride))
		return "", nil
	})
	cd.Add("repository map", func(dir string) (string, error) {
		// The old index names files the model can no longer open, so it is
		// dropped rather than kept: the next turn rebuilds it for the new
		// tree.
		r.repo.SetRoot(dir)
		return "", nil
	})
	cd.Add("memory", func(dir string) (string, error) {
		// Project memory is per project; the global scope is untouched.
		r.mem.Rekey(dir)
		return "", nil
	})
	cd.Add("skills and rules", func(dir string) (string, error) {
		// Both are read from cd.Dir() on every use, so nothing has to be
		// moved — but saying what the new directory holds is the point.
		n, m := len(r.skills()), len(r.rules())
		if n == 0 && m == 0 {
			return "", nil
		}
		return fmt.Sprintf("%d skill(s), %d rule(s)", n, m), nil
	})
	cd.Add("subagents", func(dir string) (string, error) {
		// Editing subagents check the project out into a git worktree.
		// One already running holds a checkout of the tree it was asked
		// about; taking that away mid-task would lose its work.
		if n := r.task.SetRoot(dir); n > 0 {
			return fmt.Sprintf("%d subagent(s) still working in the old tree", n), nil
		}
		return "", nil
	})
	if r.script != nil {
		cd.Add("execute_code", func(dir string) (string, error) {
			r.script.SetRoot(dir)
			return "", nil
		})
	}
	cd.Add("language servers", func(dir string) (string, error) {
		// A language server cannot be re-rooted: it is stopped, and the
		// next edit starts a fresh one in the new tree.
		if names := r.diag.Reroot(dir); len(names) > 0 {
			return "restarted " + strings.Join(names, ", "), nil
		}
		return "", nil
	})
	cd.Add("snapshots", func(dir string) (string, error) {
		// /undo and the proof notes snapshot the repository the session is
		// in. A move to somewhere that is not a git work tree leaves the
		// session without them, which is worth saying.
		repo, err := snapshot.Open(context.Background(), dir)
		if err != nil {
			r.ag.Snapshots = nil
			return "not a git repository: no snapshots or proof notes here", nil
		}
		r.ag.Snapshots = repo
		return "", nil
	})
	cd.Add("archive", func(dir string) (string, error) {
		// The archive of full tool outputs is keyed by project, but the
		// outputs in it belong to this conversation. Re-keying it would
		// make expand_output unable to read back results the model is
		// still holding ids for, so it stays where it was opened.
		return "", nil
	})
	cd.Add("running commands", func(dir string) (string, error) {
		// Processes cannot be moved between directories: a background job
		// or an open terminal keeps the directory it was started in.
		var stuck []string
		if r.jobs != nil {
			n := 0
			for _, j := range r.jobs.List() {
				if !j.Done {
					n++
				}
			}
			if n > 0 {
				stuck = append(stuck, fmt.Sprintf("%d background job(s)", n))
			}
		}
		if r.terms != nil {
			n := 0
			for _, t := range r.terms.List() {
				if !t.Done {
					n++
				}
			}
			if n > 0 {
				stuck = append(stuck, fmt.Sprintf("%d terminal(s)", n))
			}
		}
		if len(stuck) == 0 {
			return "", nil
		}
		return strings.Join(stuck, " and ") + " still in the old directory", nil
	})
}

// registerProjectRooted tells the tracker about the files the project
// itself owns — its hooks, its verification commands, its permission
// rules — plus the two stores keyed by project and the front-end's own
// idea of where it is. Each is re-discovered in the directory /cd moved
// to, and each is re-checked against the trust store there: a tree the
// user has never approved must not inherit the approval of the one they
// left.
func (r *root) registerProjectRooted(cd *agent.CD) {
	if cd == nil {
		return
	}
	cd.Add("project hooks", func(dir string) (string, error) {
		cfg, hash, err := hook.LoadProject(dir)
		if err != nil {
			return "", err
		}
		trusted := cfg.Len() > 0 && r.trust.Trusted(trust.HooksKey(dir), hash)
		runner := &hook.Runner{Config: r.cfg.Hooks, Cwd: dir, Log: func(s string) { fmt.Fprintf(os.Stderr, "[hook] %s\n", s) }}
		if trusted {
			runner.Config, _ = hook.MergeScoped(r.cfg.Hooks, cfg)
		}
		r.ag.SetHooks(runner)
		r.hooksCfg, r.hooksHash, r.hooksTrusted = cfg, hash, trusted
		if o := r.uiHooks; o != nil {
			o.Project, o.ProjectHash, o.ProjectTrusted = cfg, hash, trusted
		}
		if cfg.Len() > 0 && !trusted {
			return hook.ProjectFile + " here is not trusted yet (/hooks trust)", nil
		}
		return "", nil
	})
	cd.Add("verification", func(dir string) (string, error) {
		cfg, hash, err := verify.Load(dir)
		if err != nil {
			return "", err
		}
		defined := len(cfg.Commands) > 0 || cfg.Mutate != ""
		trusted := defined && r.trust.Trusted(trust.VerifyKey(dir), hash)
		if trusted {
			r.ag.SetVerify(&verify.Runner{Config: cfg, Cwd: dir, Mutate: mutate.ForVerify,
				Log: func(s string) { fmt.Fprintf(os.Stderr, "[verify] %s\n", s) }}, false)
		} else {
			r.ag.SetVerify(nil, defined)
		}
		r.verifyCfg, r.verifyHash, r.verifyDefined, r.verifyTrusted = cfg, hash, defined, trusted
		if o := r.uiVerify; o != nil {
			o.Config, o.ProjectHash, o.ProjectTrusted = cfg, hash, trusted
		}
		switch {
		case trusted:
			return fmt.Sprintf("%d verify command(s)", len(cfg.Commands)), nil
		case defined:
			return verify.ProjectFile + " here is not trusted yet (/verify trust)", nil
		}
		return "", nil
	})
	cd.Add("permissions", func(dir string) (string, error) {
		rules, projRules, hash, trusted, err := r.permissionRules(dir)
		if err != nil {
			return "", err
		}
		r.ag.SetRules(rules)
		// The ledger — how each rule's prompts were answered — is per
		// project too, and lives in the config directory, never in the
		// project itself.
		r.ag.SetLedger(perm.OpenLedger(r.cfgDir, memory.Key(dir)))
		r.projRules, r.permHash, r.permTrusted = projRules, hash, trusted
		if o := r.uiPerms; o != nil {
			o.Project, o.ProjectHash, o.ProjectTrusted = projRules, hash, trusted
		}
		if len(projRules.Allow) > 0 && !trusted {
			return perm.ProjectFile + " here has allow rules that are not trusted yet (/permissions trust)", nil
		}
		return "", nil
	})
	cd.Add("mcp servers", func(dir string) (string, error) {
		// A server is a process started with a working directory of its
		// own; it cannot be moved. New ones start in the new project, and
		// the project's .mcp.json is re-read and re-checked for trust,
		// but whatever is already connected stays connected.
		r.servers.Options.Root = dir
		servers, hash, err := mcp.LoadProject(dir)
		if err != nil {
			return "", err
		}
		r.projectServers, r.projectHash = servers, hash
		r.projectTrusted = len(servers) > 0 && r.trust.Trusted(dir, hash)
		if n := len(r.servers.Status()); n > 0 {
			return fmt.Sprintf("%d mcp server(s) keep the old directory", n), nil
		}
		return "", nil
	})
	cd.Add("composition root", func(dir string) (string, error) {
		r.cwd = dir
		return "", nil
	})
}
