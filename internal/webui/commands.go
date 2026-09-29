package webui

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/projectinit"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// webCommands are the shared slash commands this front-end implements, in
// palette order. Name, help and the idle flag come from internal/slash;
// webArgs only narrows the argument shape where the web version accepts
// less than the terminal's.
var webCommands = []string{
	"/help", "/clear", "/compact", "/compact-fast", "/yolo", "/auto", "/edits", "/sandbox", "/review", "/security-review", "/commit", "/pr", "/pr-comments", "/issue", "/loop",
	"/readonly", "/dryrun", "/plan", "/accept", "/verbose", "/undo", "/redo", "/changes", "/checkpoints", "/diff", "/open", "/context",
	"/stats", "/perf", "/usage", "/budget", "/todos", "/jobs", "/memory", "/remember", "/forget", "/skills", "/curator", "/init",
	"/pin", "/unpin", "/title", "/connect", "/model", "/routing", "/effort", "/fork", "/add-dir", "/btw", "/copy", "/config", "/doctor",
	"/settle", "/goal", "/why", "/trajectory", "/agents", "/cd", "/ssh", "/docker", "/reload",
	"/inspect", "/permissions", "/export", "/feedback", "/eval", "/proof", "/recipe", "/mcp", "/plugin", "/cua", "/trust", "/egress",
}

var webArgs = map[string]string{
	"/help":        "[topic]",
	"/undo":        "[preview | turn <n> [--files-only] | about <description> | apply [--force]]",
	"/accept":      "[<request> | approve | lock | unlock | command <cmd> | off]",
	"/diff":        "[path]",
	"/plan":        "[request | review | comment <line> <text> | changes | approve | off]",
	"/loop":        "[duration] [prompt|off]",
	"/jobs":        "[kill n | stdin n <text>]",
	"/memory":      "[query | pending | approve <n|name|all> | reject <n|name|all> | expire <name> | confirm [on|off]]",
	"/remember":    "[-g] <fact>",
	"/permissions": "[check <command>]",
	"/eval":        "keep [name] [--check cmd]",
	"/mcp":         "[search <words> | add <name> <command|url> | add --from <repo> --yes | remove <name> --yes]",
}

var commandSpecs = func() []slash.Command {
	out := make([]slash.Command, 0, len(webCommands))
	for _, name := range webCommands {
		c, ok := slash.Lookup(name)
		if !ok {
			continue // TestDispatchCoversWebCommands reports it
		}
		if args, ok := webArgs[name]; ok {
			c.Args = args
		}
		out = append(out, c)
	}
	return out
}()

func (s *server) commandsAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, commandSpecs)
}

// runBusy runs fn on its own goroutine with the session marked running,
// the way a turn or a recipe run is, for a command that works on the
// agent's history or undo stack and may take a while (a model call, the
// project's checks). Meanwhile a prompt is queued and an idle-only command
// refused, so fn never shares the agent with a turn — TellModelUndone and
// Compact both rewrite Messages, which a turn owns while it runs. esc
// cancels ctx. When fn returns, the first queued prompt starts.
func (s *server) runBusy(phase string, fn func(ctx context.Context)) {
	ctx, ok := s.begin(phase)
	if !ok {
		s.errf("%s waits for the turn to finish (esc cancels it)", phase)
		return
	}
	s.emitStatus()
	go func() {
		fn(ctx)
		next, nextCtx, _ := s.end(false)
		s.emitStatus()
		if next != "" {
			s.runTurn(nextCtx, next)
		}
	}()
}

// webHandler runs one command with arg, the line after its name, trimmed.
// When done is true, err is runCommand's answer as it stands; otherwise the
// status is re-sent and runCommand returns nil.
type webHandler func(s *server, arg string) (done bool, err error)

// webDispatch maps every name runCommand answers to, aliases included, to
// its handler. TestDispatchCoversWebCommands checks it against webCommands.
var webDispatch = map[string]webHandler{
	"/help": func(s *server, arg string) (bool, error) {
		s.help(arg)
		return false, nil
	},
	"/clear": (*server).cmdClear, "/new": (*server).cmdClear,
	"/yolo":    (*server).cmdYolo,
	"/routing": (*server).cmdRouting,
	"/auto":    (*server).cmdAuto,
	"/sandbox": func(s *server, arg string) (bool, error) {
		s.sandbox(arg)
		return false, nil
	},
	"/review": func(s *server, arg string) (bool, error) {
		s.review(arg)
		return false, nil
	},
	"/security-review": func(s *server, arg string) (bool, error) {
		s.securityReview(arg)
		return false, nil
	},
	"/commit": func(s *server, arg string) (bool, error) {
		s.gitCommand("/commit", arg)
		return false, nil
	},
	"/pr": func(s *server, arg string) (bool, error) {
		s.gitCommand("/pr", arg)
		return false, nil
	},
	"/pr-comments": func(s *server, arg string) (bool, error) {
		s.gitCommand("/pr-comments", arg)
		return false, nil
	},
	"/issue": func(s *server, arg string) (bool, error) {
		s.gitCommand("/issue", arg)
		return false, nil
	},
	"/loop": func(s *server, arg string) (bool, error) {
		s.loop(arg)
		return false, nil
	},
	"/edits":    (*server).cmdEdits,
	"/readonly": (*server).cmdReadonly,
	"/dryrun": func(s *server, arg string) (bool, error) {
		ag := s.opts.Agent
		if first, _, _ := strings.Cut(strings.TrimSpace(arg), " "); strings.EqualFold(first, "apply") {
			s.runBusy("applying the dry-run preview", func(ctx context.Context) {
				s.showResult(ag.DryRunCommand(ctx, arg))
			})
			return false, nil
		}
		s.showResult(ag.DryRunCommand(s.life, arg))
		s.emitStatus()
		return false, nil
	},
	"/plan": func(s *server, arg string) (bool, error) {
		s.plan(arg)
		return false, nil
	},
	"/accept": func(s *server, arg string) (bool, error) {
		s.accept(arg)
		return false, nil
	},
	"/verbose":      (*server).cmdVerbose,
	"/compact":      (*server).cmdCompact,
	"/compact-fast": (*server).cmdCompactFast, "/compress-fast": (*server).cmdCompactFast,
	"/undo": (*server).cmdUndo,
	"/redo": (*server).cmdRedo,
	"/checkpoints": func(s *server, arg string) (bool, error) {
		s.sheet("checkpoints", agent.FormatCheckpoints(s.cwd(), s.opts.Agent.CheckpointList()))
		return false, nil
	},
	"/changes": func(s *server, arg string) (bool, error) {
		s.sheet("changes", s.changesText(arg))
		return false, nil
	},
	"/diff": func(s *server, arg string) (bool, error) {
		s.sheet("diff", s.diffText(arg))
		return false, nil
	},
	"/open": func(s *server, arg string) (bool, error) {
		s.sheet("open", s.openText(arg))
		return false, nil
	},
	"/context": func(s *server, arg string) (bool, error) {
		s.sheet("context", s.contextText())
		return false, nil
	},
	"/stats": func(s *server, arg string) (bool, error) {
		s.sheet("stats", s.statsText())
		return false, nil
	},
	"/perf": func(s *server, arg string) (bool, error) {
		s.card("perf", s.perfCard())
		return false, nil
	},
	"/usage": func(s *server, arg string) (bool, error) {
		s.card("usage", s.usageCard())
		return false, nil
	},
	"/effort": func(s *server, arg string) (bool, error) {
		s.effort(arg)
		return false, nil
	},
	"/budget": func(s *server, arg string) (bool, error) {
		s.budget(arg)
		return false, nil
	},
	"/todos": func(s *server, arg string) (bool, error) {
		s.sheet("todos", s.todosText())
		return false, nil
	},
	"/jobs": func(s *server, arg string) (bool, error) {
		s.jobs(arg)
		return false, nil
	},
	"/memory": func(s *server, arg string) (bool, error) {
		s.memory("/memory", arg)
		return false, nil
	},
	"/remember": func(s *server, arg string) (bool, error) {
		s.memory("/remember", arg)
		return false, nil
	},
	"/forget": func(s *server, arg string) (bool, error) {
		s.memory("/forget", arg)
		return false, nil
	},
	"/skills": func(s *server, arg string) (bool, error) {
		s.sheet("skills", s.skillsText())
		return false, nil
	},
	"/curator": func(s *server, arg string) (bool, error) {
		s.curator(arg)
		return false, nil
	},
	"/init": func(s *server, arg string) (bool, error) {
		s.showResult(projectinit.Run(s.opts.Cwd, strings.EqualFold(strings.TrimSpace(arg), "force")))
		return false, nil
	},
	"/pin": func(s *server, arg string) (bool, error) {
		s.pin(arg)
		return false, nil
	},
	"/unpin": (*server).cmdUnpin,
	"/title": (*server).cmdTitle,
	"/inspect": func(s *server, arg string) (bool, error) {
		s.inspect(arg)
		return false, nil
	},
	"/connect": func(s *server, arg string) (bool, error) {
		s.emit(envelope{Type: "connect"})
		return false, nil
	},
	"/model": func(s *server, arg string) (bool, error) {
		s.model(arg)
		return false, nil
	},
	"/fork": (*server).cmdFork,
	"/add-dir": func(s *server, arg string) (bool, error) {
		s.addDir(arg)
		return false, nil
	},
	"/btw": func(s *server, arg string) (bool, error) {
		s.btw(arg)
		return false, nil
	},
	"/copy": func(s *server, arg string) (bool, error) {
		s.copyLast()
		return false, nil
	},
	"/config": func(s *server, arg string) (bool, error) {
		s.configKV(arg)
		return false, nil
	},
	"/doctor": func(s *server, arg string) (bool, error) {
		s.doctor()
		return false, nil
	},
	"/settle": (*server).cmdSettle,
	"/goal": func(s *server, arg string) (bool, error) {
		s.goal(arg)
		return false, nil
	},
	"/why": (*server).cmdWhy,
	"/trajectory": func(s *server, arg string) (bool, error) {
		s.trajectory(arg)
		return false, nil
	},
	"/agents": func(s *server, arg string) (bool, error) {
		s.agents(arg)
		return false, nil
	},
	"/cd": func(s *server, arg string) (bool, error) {
		s.cd(arg)
		return false, nil
	},
	"/ssh": func(s *server, arg string) (bool, error) {
		s.retarget("ssh", arg, tool.DialSSH)
		return false, nil
	},
	"/docker": func(s *server, arg string) (bool, error) {
		s.retarget("docker", arg, tool.DialDocker)
		return false, nil
	},
	"/reload": (*server).cmdReload,
	"/permissions": func(s *server, arg string) (bool, error) {
		// check <command> answers what the rules would decide, as in the
		// terminal; anything else shows the summary.
		if f := strings.Fields(arg); len(f) >= 2 && f[0] == "check" {
			s.notice(s.opts.Agent.PermissionCheck(f[1:]))
			return false, nil
		}
		s.sheet("permissions", s.permissionsText())
		return false, nil
	},
	"/export": func(s *server, arg string) (bool, error) {
		s.export(arg)
		return false, nil
	},
	"/eval": func(s *server, arg string) (bool, error) {
		s.evalKeep(arg)
		return false, nil
	},
	"/proof": func(s *server, arg string) (bool, error) {
		s.proofCommand(arg)
		return false, nil
	},
	"/recipe": func(s *server, arg string) (bool, error) {
		s.recipeCommand(arg)
		return false, nil
	},
	"/mcp": func(s *server, arg string) (bool, error) {
		s.mcpCommand(arg)
		return false, nil
	},
	"/plugin": func(s *server, arg string) (bool, error) {
		s.pluginCommand(strings.Fields(arg))
		return false, nil
	},
	"/cua": (*server).cmdCua,
	"/egress": func(s *server, arg string) (bool, error) {
		s.sheet("egress", s.opts.Agent.EgressSummary())
		return false, nil
	},
	"/trust": func(s *server, arg string) (bool, error) {
		s.trustCommand(arg)
		return false, nil
	},
	"/feedback": func(s *server, arg string) (bool, error) {
		s.notice("recorded " + arg + " (not sent to the model)")
		return false, nil
	},
}

func (s *server) runCommand(text string) error {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
	}
	name, arg := fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	// The idle-only check is on the canonical name: /new is /clear, and
	// matching only the table names let every alias straight past it.
	canon := slash.Canonical(name)
	for _, c := range commandSpecs {
		if c.Name == canon && c.Idle && running {
			return fmt.Errorf("%s waits for the turn to finish (esc cancels it)", name)
		}
	}
	h, ok := webDispatch[name]
	if !ok {
		names := make([]string, 0, len(webDispatch))
		for n := range webDispatch {
			names = append(names, n)
		}
		sort.Strings(names) // Nearest keeps the first of equal scores
		if near := textfmt.Nearest(name, names); near != "" {
			s.errf("unknown command %s · did you mean %s?", name, near)
		} else {
			s.errf("unknown command %s (/help lists them)", name)
		}
		s.emitStatus()
		return nil
	}
	if done, err := h(s, arg); done {
		return err
	}
	s.emitStatus()
	return nil
}

func (s *server) cmdClear(arg string) (bool, error) {
	return true, s.startNewSession()
}

func (s *server) cmdYolo(arg string) (bool, error) {
	ag := s.opts.Agent
	if !ag.Settings().AutoApprove {
		if err := ag.ModeRefusal(managed.ModeYolo); err != nil {
			return false, err
		}
	}
	var on bool
	ag.UpdateSettings(func(st *agent.Settings) {
		st.AutoApprove = !st.AutoApprove
		if on = st.AutoApprove; on {
			st.AutoMode = false
		}
	})
	s.noteOnOff("auto-approve", on, "every tool runs without asking")
	return false, nil
}

// cmdRouting is the web twin of the terminal's /routing: it vetoes the
// step router for the rest of the session, so every model call goes to
// the session's own model whatever route_cheap names.
func (s *server) cmdRouting(arg string) (bool, error) {
	veto := s.opts.Agent.ToggleOff("routing")
	s.noteOnOff("step routing", !veto, "every call goes to the session's own model; /routing again re-enables it")
	return false, nil
}

func (s *server) cmdAuto(arg string) (bool, error) {
	ag := s.opts.Agent
	if !ag.Settings().AutoMode {
		if err := ag.ModeRefusal(managed.ModeAuto); err != nil {
			return false, err
		}
	}
	var on bool
	ag.UpdateSettings(func(st *agent.Settings) {
		st.AutoMode = !st.AutoMode
		if on = st.AutoMode; on {
			st.AutoApprove = false
		}
	})
	if on {
		ag.SetPlanMode(false)
	}
	s.noteOnOff("auto mode", on, "in-workspace edits and known-safe reads run, other commands ask")
	return false, nil
}

func (s *server) cmdEdits(arg string) (bool, error) {
	ag := s.opts.Agent
	if !ag.Settings().AutoEdits {
		if err := ag.ModeRefusal(managed.ModeEdits); err != nil {
			return false, err
		}
	}
	var on bool
	ag.UpdateSettings(func(st *agent.Settings) { st.AutoEdits = !st.AutoEdits; on = st.AutoEdits })
	s.noteOnOff("auto-accept edits", on, "file changes go through, commands still ask")
	return false, nil
}

func (s *server) cmdReadonly(arg string) (bool, error) {
	ag := s.opts.Agent
	var on bool
	ag.UpdateSettings(func(st *agent.Settings) { st.ReadOnly = !st.ReadOnly; on = st.ReadOnly })
	s.noteOnOff("read-only", on, "the model can inspect but change nothing")
	return false, nil
}

func (s *server) cmdVerbose(arg string) (bool, error) {
	s.mu.Lock()
	s.verbose = !s.verbose
	v := s.verbose
	s.mu.Unlock()
	s.noteOnOff("verbose tool output", v, "every tool result is shown whole")
	return false, nil
}

func (s *server) cmdCompact(arg string) (bool, error) {
	ag := s.opts.Agent
	s.runBusy("compacting context", func(ctx context.Context) { _ = ag.CompactManual(ctx, s.apply) })
	return false, nil
}

func (s *server) cmdCompactFast(arg string) (bool, error) {
	ag := s.opts.Agent
	s.runBusy("pruning context", func(ctx context.Context) { _ = ag.CompactFast(ctx, s.apply) })
	return false, nil
}

func (s *server) cmdUndo(arg string) (bool, error) {
	ag := s.opts.Agent
	// One shared parser (agent.ParseUndoArgs) decides what "/undo
	// ..." means, so the terminal and the web UI agree; the web UI
	// just does not expose "/undo n" as its own command (webArgs
	// narrows it out), so ua.N falls through to a bare undo below,
	// same as an unrecognized argument always has.
	ua, perr := agent.ParseUndoArgs(arg)
	if perr != nil {
		s.errf("%v", perr)
		return true, nil
	}
	switch {
	case ua.Preview:
		list, err := ag.UndoPreview(s.life)
		if err != nil {
			s.errf("undo: %v", err)
			return true, nil
		}
		s.sheet("undo preview", strings.Join(list, "\n"))
		return true, nil
	case ua.About:
		if ua.Description == "" {
			s.errf("%v", agent.ErrUndoAboutUsage)
			return true, nil
		}
		s.runBusy("matching changes", func(ctx context.Context) {
			s.showResult(ag.UndoAbout(ctx, s.cwd(), ua.Description))
		})
		return true, nil
	case ua.Turn != 0:
		s.runBusy("listing what a restore would change", func(ctx context.Context) {
			s.showResult(ag.UndoToTurn(ctx, s.cwd(), ua.Turn, ua.FilesOnly))
		})
		return true, nil
	case ua.Apply:
		s.runBusy("applying undo", func(ctx context.Context) {
			rep, res, err := ag.UndoApply(ctx, ua.Force)
			s.showResult(res, err)
			if err == nil {
				ag.TellModelUndone(rep)
			}
		})
		return true, nil
	}
	done, err := ag.Undo(s.life)
	if err != nil {
		s.errf("undo: %v", err)
		return true, nil
	}
	s.notice("↶ " + strings.Join(done, ", "))
	return false, nil
}

func (s *server) cmdRedo(arg string) (bool, error) {
	ag := s.opts.Agent
	done, err := ag.Redo(s.life)
	if err != nil {
		s.errf("redo: %v", err)
		return true, nil
	}
	s.notice("↷ " + strings.Join(done, ", "))
	return false, nil
}

func (s *server) cmdUnpin(arg string) (bool, error) {
	ag := s.opts.Agent
	n, err := strconv.Atoi(arg)
	if err != nil || !ag.Unpin(n) {
		s.errf("no pin %s; /pin lists them", arg)
		return true, nil
	}
	s.notice("unpinned")
	return false, nil
}

func (s *server) cmdTitle(arg string) (bool, error) {
	if arg == "" || s.sess() == nil {
		s.errf("usage: /title <text>")
		return true, nil
	}
	if err := s.sess().SetTitle(arg); err != nil {
		s.errf("title: %v", err)
		return true, nil
	}
	s.notice("titled: " + arg)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	return false, nil
}

func (s *server) cmdFork(arg string) (bool, error) {
	if err := s.fork(arg); err != nil {
		s.errf("fork: %v", err)
		return true, nil
	}
	return false, nil
}

func (s *server) cmdSettle(arg string) (bool, error) {
	if s.sess() == nil {
		s.errf("no session to settle")
		return true, nil
	}
	settled, err := s.sess().ToggleSettled()
	if err != nil {
		s.errf("settle: %v", err)
		return true, nil
	}
	if settled {
		s.notice("session settled: it lists after the active ones in the sidebar until something happens in it")
	} else {
		s.notice("session active again")
	}
	return false, nil
}

func (s *server) cmdWhy(arg string) (bool, error) {
	n := 1
	if arg != "" {
		v, err := strconv.Atoi(arg)
		if err != nil || v < 1 {
			s.errf("usage: /why [n] · n counts back from the most recent")
			return true, nil
		}
		n = v
	}
	if s.sess() == nil {
		s.errf("no session")
		return true, nil
	}
	s.sheet("why", agent.WhyLast(s.sess().Events(), n))
	return false, nil
}

func (s *server) cmdReload(arg string) (bool, error) {
	ag := s.opts.Agent
	ag.RefreshExtra()
	s.notice("memory and skill indexes will be rebuilt for the next call (one cache miss); they also refresh at compaction")
	return false, nil
}
