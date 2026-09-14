package webui

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/tool"
)

// webCommands are the shared slash commands this front-end implements, in
// palette order. Name, help and the idle flag come from internal/slash;
// webArgs only narrows the argument shape where the web version accepts
// less than the terminal's.
var webCommands = []string{
	"/help", "/clear", "/compact", "/yolo", "/auto", "/edits", "/sandbox", "/review", "/loop",
	"/readonly", "/plan", "/accept", "/verbose", "/undo", "/redo", "/changes", "/diff", "/open", "/context",
	"/stats", "/usage", "/budget", "/todos", "/jobs", "/memory", "/remember", "/forget", "/skills",
	"/pin", "/unpin", "/title", "/model", "/fork", "/add-dir", "/btw", "/copy", "/config", "/doctor",
	"/settle", "/goal", "/why", "/trajectory", "/agents", "/ssh", "/docker", "/reload",
	"/inspect", "/permissions", "/export", "/feedback", "/eval", "/proof", "/recipe",
}

var webArgs = map[string]string{
	"/help":        "[topic]",
	"/undo":        "[preview | about <description> | apply [--force]]",
	"/accept":      "[<request> | approve | lock | unlock | command <cmd> | off]",
	"/diff":        "[path]",
	"/loop":        "[duration] [prompt|off]",
	"/jobs":        "[kill n]",
	"/memory":      "[query]",
	"/remember":    "[-g] <fact>",
	"/permissions": "",
	"/eval":        "keep [name] [--check cmd]",
}

var commandSpecs = func() []slash.Command {
	out := make([]slash.Command, 0, len(webCommands))
	for _, name := range webCommands {
		c, ok := slash.Lookup(name)
		if !ok {
			panic("webui: " + name + " is not in the slash table")
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
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.errf("%s waits for the turn to finish (esc cancels it)", phase)
		return
	}
	s.running = true
	s.phase = phase
	s.started = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()
	s.emitStatus()
	go func() {
		fn(ctx)
		s.mu.Lock()
		s.running = false
		s.phase = ""
		if s.cancel != nil {
			s.cancel()
			s.cancel = nil
		}
		var next string
		if len(s.queued) > 0 {
			next = s.queued[0]
			s.queued = s.queued[1:]
		}
		s.mu.Unlock()
		s.emitStatus()
		if next != "" {
			s.startTurn(next)
		}
	}()
}

func (s *server) runCommand(text string) error {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return fmt.Errorf("empty command")
	}
	name, arg := fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	ag := s.opts.Agent
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	for _, c := range commandSpecs {
		if c.Name == name && c.Idle && running {
			return fmt.Errorf("%s waits for the turn to finish (esc cancels it)", name)
		}
	}
	switch name {
	case "/help":
		s.help(arg)
	case "/clear", "/new":
		return s.startNewSession()
	case "/yolo":
		var on bool
		ag.UpdateSettings(func(st *agent.Settings) {
			st.AutoApprove = !st.AutoApprove
			if on = st.AutoApprove; on {
				st.AutoMode = false
			}
		})
		s.noteOnOff("auto-approve", on, "every tool runs without asking")
	case "/auto":
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
	case "/sandbox":
		s.sandbox(arg)
	case "/review":
		s.review(arg)
	case "/loop":
		s.loop(arg)
	case "/edits":
		var on bool
		ag.UpdateSettings(func(st *agent.Settings) { st.AutoEdits = !st.AutoEdits; on = st.AutoEdits })
		s.noteOnOff("auto-accept edits", on, "file changes go through, commands still ask")
	case "/readonly":
		var on bool
		ag.UpdateSettings(func(st *agent.Settings) { st.ReadOnly = !st.ReadOnly; on = st.ReadOnly })
		s.noteOnOff("read-only", on, "the model can inspect but change nothing")
	case "/plan":
		s.plan(arg)
	case "/accept":
		s.accept(arg)
	case "/verbose":
		s.mu.Lock()
		s.verbose = !s.verbose
		v := s.verbose
		s.mu.Unlock()
		s.noteOnOff("verbose tool output", v, "every tool result is shown whole")
	case "/compact":
		s.runBusy("compacting context", func(ctx context.Context) { _ = ag.Compact(ctx, s.apply) })
	case "/undo":
		// One shared parser (agent.ParseUndoArgs) decides what "/undo
		// ..." means, so the terminal and the web UI agree; the web UI
		// just does not expose "/undo n" as its own command (webArgs
		// narrows it out), so ua.N falls through to a bare undo below,
		// same as an unrecognized argument always has.
		ua, perr := agent.ParseUndoArgs(arg)
		if perr != nil {
			s.errf("%v", perr)
			return nil
		}
		switch {
		case ua.Preview:
			list, err := ag.UndoPreview()
			if err != nil {
				s.errf("undo: %v", err)
				return nil
			}
			s.sheet("undo preview", strings.Join(list, "\n"))
			return nil
		case ua.About:
			if ua.Description == "" {
				s.errf("usage: /undo about <description>")
				return nil
			}
			s.runBusy("matching changes", func(ctx context.Context) {
				ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				sel, err := ag.PreviewUndoAbout(ctx, ua.Description)
				if err != nil {
					s.errf("undo about: %v", err)
					return
				}
				s.sheet("undo about", agent.FormatSelection(s.opts.Cwd, sel))
			})
			return nil
		case ua.Apply:
			s.runBusy("applying undo", func(ctx context.Context) {
				ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				rep, err := ag.ApplyPendingUndo(ctx, ua.Force)
				if err != nil {
					s.errf("undo apply: %v", err)
					return
				}
				s.sheet("undo", agent.FormatUndoReport(rep))
				ag.TellModelUndone(rep)
			})
			return nil
		}
		done, err := ag.Undo()
		if err != nil {
			s.errf("undo: %v", err)
			return nil
		}
		s.notice("↶ " + strings.Join(done, ", "))
	case "/redo":
		done, err := ag.Redo()
		if err != nil {
			s.errf("redo: %v", err)
			return nil
		}
		s.notice("↷ " + strings.Join(done, ", "))
	case "/changes":
		s.sheet("changes", s.changesText(arg))
	case "/diff":
		s.sheet("diff", s.diffText(arg))
	case "/open":
		s.sheet("open", s.openText(arg))
	case "/context":
		s.sheet("context", s.contextText())
	case "/stats":
		s.sheet("stats", s.statsText())
	case "/usage":
		s.sheet("usage", s.usageText())
	case "/budget":
		s.budget(arg)
	case "/todos":
		s.sheet("todos", s.todosText())
	case "/jobs":
		s.jobs(arg)
	case "/memory":
		s.memory("/memory", arg)
	case "/remember":
		s.memory("/remember", arg)
	case "/forget":
		s.memory("/forget", arg)
	case "/skills":
		s.sheet("skills", s.skillsText())
	case "/pin":
		s.pin(arg)
	case "/unpin":
		n, err := strconv.Atoi(arg)
		if err != nil || !ag.Unpin(n) {
			s.errf("no pin %s; /pin lists them", arg)
			return nil
		}
		s.notice("unpinned")
	case "/title":
		if arg == "" || s.opts.Session == nil {
			s.errf("usage: /title <text>")
			return nil
		}
		if err := s.opts.Session.SetTitle(arg); err != nil {
			s.errf("title: %v", err)
			return nil
		}
		s.notice("titled: " + arg)
		s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	case "/inspect":
		s.inspect(arg)
	case "/model":
		s.model(arg)
	case "/fork":
		if err := s.fork(arg); err != nil {
			s.errf("fork: %v", err)
			return nil
		}
	case "/add-dir":
		s.addDir(arg)
	case "/btw":
		s.btw(arg)
	case "/copy":
		s.copyLast()
	case "/config":
		s.configKV(arg)
	case "/doctor":
		s.doctor()
	case "/settle":
		if s.opts.Session == nil {
			s.errf("no session to settle")
			return nil
		}
		settled, err := s.opts.Session.ToggleSettled()
		if err != nil {
			s.errf("settle: %v", err)
			return nil
		}
		if settled {
			s.notice("session settled: it lists after the active ones in the sidebar until something happens in it")
		} else {
			s.notice("session active again")
		}
	case "/goal":
		s.goal(arg)
	case "/why":
		n := 1
		if arg != "" {
			v, err := strconv.Atoi(arg)
			if err != nil || v < 1 {
				s.errf("usage: /why [n] · n counts back from the most recent")
				return nil
			}
			n = v
		}
		if s.opts.Session == nil {
			s.errf("no session")
			return nil
		}
		s.sheet("why", agent.WhyLast(s.opts.Session.Events(), n))
	case "/trajectory":
		s.trajectory(arg)
	case "/agents":
		s.agents(arg)
	case "/ssh":
		s.retarget("ssh", arg, tool.DialSSH)
	case "/docker":
		s.retarget("docker", arg, tool.DialDocker)
	case "/reload":
		ag.RefreshExtra()
		s.notice("memory and skill indexes will be rebuilt for the next call (one cache miss); they also refresh at compaction")
	case "/permissions":
		s.sheet("permissions", s.permissionsText())
	case "/export":
		s.export(arg)
	case "/eval":
		s.evalKeep(arg)
	case "/proof":
		s.proofCommand(arg)
	case "/recipe":
		s.recipeCommand(arg)
	case "/feedback":
		s.notice("recorded " + arg + " (not sent to the model)")
	default:
		s.errf("unknown command %s (try /help)", name)
	}
	s.emitStatus()
	return nil
}
