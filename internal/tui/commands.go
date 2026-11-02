package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/claudeimport"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/keymap"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/managed"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/projectinit"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
)

type command struct {
	name, args, help string
	// keyHelp is the description's clause naming a terminal key; it is
	// shown with the key the user bound (commandDesc).
	keyHelp string
	// argsFor lists the completions for this command's argument, when
	// they can be enumerated.
	argsFor func(m *model, partial string) []command
}

// argCompleters lists the commands whose argument can be enumerated.
var argCompleters = map[string]func(m *model, partial string) []command{
	"/features": (*model).completeFeatures,
	"/jev":      (*model).completeJev,
	"/resume":   (*model).completeSession,
	"/model":    (*model).completeModel,
	"/theme":    (*model).completeTheme,
	"/forget":   (*model).completeMemory,
	"/undo":     (*model).completeUndo,
	"/open":     (*model).completeFile,
	"/diff":     (*model).completeFile,
	"/agents":   (*model).completeAgents,
	"/jobs":     (*model).completeJobs,
}

// commands is the shared slash table with the terminal's completions.
var commands = func() []command {
	out := make([]command, 0, len(slash.Commands))
	for _, c := range slash.Commands {
		out = append(out, command{name: c.Name, args: c.Args, help: c.Help, keyHelp: c.KeyHelp, argsFor: argCompleters[c.Name]})
	}
	return out
}()

// lookup finds a built-in command by name.
func lookup(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// cmdHandler runs one built-in command. text is the whole line and fields
// its words, fields[0] being the name typed. When done is true, cmd is the
// dispatcher's answer as it stands; otherwise the view is refreshed and the
// dispatcher returns nil.
type cmdHandler func(m *model, text string, fields []string) (cmd tea.Cmd, done bool)

// cmdArg is the line after the command name, trimmed.
func cmdArg(text string, fields []string) string {
	return strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
}

// dispatch maps every built-in command name, aliases included, to its
// handler. The drift test checks it against slash.Commands and slash.Aliases.
var dispatch = map[string]cmdHandler{
	"/help": (*model).cmdHelp, "/?": (*model).cmdHelp,
	"/quit": (*model).cmdQuit, "/exit": (*model).cmdQuit, "/q": (*model).cmdQuit,
	"/clear": (*model).cmdClear, "/new": (*model).cmdClear,
	"/yolo":    (*model).cmdYolo,
	"/routing": (*model).cmdRouting,
	"/auto":    (*model).cmdAuto,
	"/import-claude": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		home, _ := os.UserHomeDir()
		m.showResult(claudeimport.Exec(claudeimport.Env{Home: home, Cwd: m.opts.Cwd, ConfigPath: m.opts.ConfigPath, ConfigDir: m.opts.ConfigDir}, cmdArg(text, fields)), nil)
		return nil, false
	},
	"/sandbox": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.sandboxCommand(cmdArg(text, fields))
		return nil, false
	},
	"/review": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.reviewCommand(cmdArg(text, fields)), true
	},
	"/security-review": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.securityReviewCommand(cmdArg(text, fields))
		return nil, false
	},
	"/pr-comments": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.gitCommand("/pr-comments", cmdArg(text, fields))
		return nil, false
	},
	"/issue": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.gitCommand("/issue", cmdArg(text, fields))
		return nil, false
	},
	"/loop": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.loopCommand(cmdArg(text, fields))
		return nil, false
	},
	"/voice": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		cmd := m.voiceCommand(cmdArg(text, fields))
		m.refresh(false)
		return cmd, true
	},
	"/edits":       (*model).cmdEdits,
	"/settle":      (*model).cmdSettle,
	"/readonly":    (*model).cmdReadonly,
	"/dryrun":      (*model).cmdDryrun,
	"/checkpoints": (*model).cmdCheckpoints,
	"/plan": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.planCommand(cmdArg(text, fields)), true
	},
	"/accept": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.acceptCommand(cmdArg(text, fields)), true
	},
	"/why": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.whyCommand(fields[1:])
		return nil, false
	},
	"/trajectory": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.trajectoryCommand(fields[1:])
		return nil, false
	},
	"/feedback": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.feedbackCommand(fields[1:])
		return nil, false
	},
	"/goal": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.goalCommand(cmdArg(text, fields)), true
	},
	"/agents": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.agentsCommand(cmdArg(text, fields))
		return nil, false
	},
	"/personas": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.transcript.code(agent.PersonaReport(m.opts.Agent))
		return nil, false
	},
	"/cd": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.cdCommand(cmdArg(text, fields))
		return nil, false
	},
	"/ssh": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.sshCommand(cmdArg(text, fields))
		return nil, false
	},
	"/docker": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.dockerCommand(cmdArg(text, fields))
		return nil, false
	},
	"/theme": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		if cmd := m.themeCommand(fields[1:]); cmd != nil {
			return cmd, true
		}
		return nil, false
	},
	"/hints": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.hintsCommand(fields[1:])
		return nil, false
	},
	"/cua": (*model).cmdCua,
	"/followup": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.followupCommand(fields[1:])
		return nil, false
	},
	"/verbose": (*model).cmdVerbose,
	"/vim":     (*model).cmdVim,
	"/connect": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.openConnect(stepPick), true
	},
	"/model":    (*model).cmdModel,
	"/features": (*model).cmdFeatures,
	"/jev":      (*model).cmdJev,
	"/plugin": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.pluginCommand(fields[1:]), true
	},
	"/mcp": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		// Quotes keep a header value with a space in it one argument.
		args, err := mcp.SplitArgs(strings.TrimPrefix(strings.TrimSpace(text), fields[0]))
		if err != nil {
			m.transcript.errorf("/mcp: %v", err)
			return nil, false
		}
		if cmd := m.mcpCommand(args); cmd != nil {
			return cmd, true
		}
		return nil, false
	},
	"/egress": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.transcript.info(m.opts.Agent.EgressSummary())
		return nil, false
	},
	"/trust": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		if cmd := m.trustCommand(fields[1:]); cmd != nil {
			return cmd, true
		}
		return nil, false
	},
	"/hooks": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.hooksCommand(fields[1:])
		return nil, false
	},
	"/permissions": (*model).cmdPermissions, "/perms": (*model).cmdPermissions,
	"/lsp": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.lspCommand()
		return nil, false
	},
	"/jobs": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.jobsCommand(fields[1:])
		return nil, false
	},
	"/open": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.openCommand(cmdArg(text, fields))
		return nil, false
	},
	"/diff": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.diffCommand(fields[1:])
		return nil, false
	},
	"/fork": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.forkSession(cmdArg(text, fields))
		return nil, false
	},
	"/add-dir": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.addDirCommand(cmdArg(text, fields))
		return nil, false
	},
	"/recap": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.recapCommand(false), true
	},
	"/btw": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.btwCommand(cmdArg(text, fields)), true
	},
	"/copy": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.copyCommand()
		return nil, false
	},
	"/config": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.configCommand(cmdArg(text, fields))
		return nil, false
	},
	"/doctor": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.doctorCommand(), true
	},
	"/title": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.setTitle(cmdArg(text, fields))
		return nil, false
	},
	"/history": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.searchSessions(cmdArg(text, fields))
		return nil, false
	},
	"/reload":  (*model).cmdReload,
	"/context": (*model).cmdContext, "/ctx": (*model).cmdContext,
	"/todos": (*model).cmdTodos, "/todo": (*model).cmdTodos,
	"/inspect": (*model).cmdInspect, "/i": (*model).cmdInspect,
	"/pin": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.pinCommand(cmdArg(text, fields))
		return nil, false
	},
	"/unpin": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.unpinCommand(fields[1:])
		return nil, false
	},
	"/verify": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		if cmd := m.verifyCommand(fields[1:]); cmd != nil {
			return cmd, true
		}
		return nil, false
	},
	"/init":     (*model).cmdInit,
	"/skills":   (*model).cmdSkills,
	"/curator":  (*model).cmdCurator,
	"/rules":    (*model).cmdRules,
	"/memory":   (*model).cmdMemory,
	"/remember": (*model).cmdMemory,
	"/forget":   (*model).cmdMemory,
	"/dream":    (*model).cmdMemory,
	"/sleep":    (*model).cmdMemory,
	"/undo":     (*model).cmdUndo,
	"/redo":     (*model).cmdUndo,
	"/changes": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.changesCommand(fields[1:])
		return nil, false
	},
	"/tour": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.transcript.code(m.opts.Agent.TourText(m.life, cmdArg(text, fields), m.opts.Cwd))
		return nil, false
	},
	"/blame": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.blameCommand(cmdArg(text, fields))
		return nil, false
	},
	"/find": (*model).cmdFind, "/search": (*model).cmdFind,
	"/resume": (*model).cmdResume, "/sessions": (*model).cmdResume,
	"/recover":      (*model).cmdRecover,
	"/compact":      (*model).cmdCompact,
	"/compact-fast": (*model).cmdCompactFast, "/compress-fast": (*model).cmdCompactFast,
	"/export": (*model).cmdExport,
	"/stats": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.statsCommand()
		return nil, false
	},
	"/perf": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.perfCommand()
		return nil, false
	},
	"/usage": (*model).cmdUsage, "/cost": (*model).cmdUsage,
	"/effort": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.effortCommand(cmdArg(text, fields))
		return nil, false
	},
	"/budget": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.budgetCommand(cmdArg(text, fields))
		return nil, false
	},
	"/eval": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.evalCommand(cmdArg(text, fields)), true
	},
	"/commit": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.shipCommand("committing", m.opts.Agent.CommitCommand, cmdArg(text, fields)), true
	},
	"/pr": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.shipCommand("opening the pull request", m.opts.Agent.PRCommand, cmdArg(text, fields)), true
	},
	"/proof": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.proofCommand(cmdArg(text, fields))
		return nil, false
	},
	"/recipe": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.recipeCommand(cmdArg(text, fields)), true
	},
}

func (m *model) command(text string) tea.Cmd {
	// A paste chip after a command name is its argument like any other
	// text: /btw, /goal, /loop and the rest used to get the chip itself,
	// and the ones that keep their argument kept a chip nothing could
	// inflate once this process was gone.
	text = m.inflatePastes(text)
	fields := strings.Fields(text)
	// Idle-only is decided by the slash table alone, on the canonical
	// name: /new is /clear, and gating only the table names let every
	// alias of an idle-only command straight past the check.
	if m.turn.running {
		if c, ok := slash.Lookup(slash.Canonical(fields[0])); ok && c.Idle {
			m.transcript.errorf("%s", m.waitsForTurn(fields[0]))
			m.refresh(false)
			return nil
		}
	}
	m.noteUse(fields[0])
	defer m.observeCommand(fields)
	if h, ok := dispatch[fields[0]]; ok {
		if cmd, done := h(m, text, fields); done {
			return cmd
		}
	} else if cmd, ok := m.runUserCommand(text, fields); ok {
		return cmd
	} else {
		var names []string
		for _, c := range m.allCommands() {
			names = append(names, c.name)
		}
		if near := textfmt.Nearest(fields[0], names); near != "" {
			m.transcript.errorf("unknown command %s · did you mean %s?", fields[0], near)
		} else {
			m.transcript.errorf("unknown command %s (/help lists them)", fields[0])
		}
	}
	m.refresh(false)
	return nil
}

// runUserCommand runs a name the built-in table does not know: a custom
// command, a user-invocable skill, or an MCP prompt, in that order.
func (m *model) runUserCommand(text string, fields []string) (tea.Cmd, bool) {
	if c, ok := m.custom[fields[0]]; ok {
		if m.waitForTurn(fields[0]) {
			return nil, true
		}
		args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		return m.submit(c.Expand(args)), true
	}
	for _, sk := range skill.WithBuiltins(skill.Load(m.opts.Cwd, m.opts.ConfigDir)) {
		if sk.Blocked && "/"+sk.Name == fields[0] {
			m.note("skill " + sk.Name + " is blocked: " + skill.Blocking(sk.Findings)[0].String() + "; /skills says more")
			return nil, true
		}
		if sk.UserInvocable && "/"+sk.Name == fields[0] {
			if m.waitForTurn(fields[0]) {
				return nil, true
			}
			m.note("running skill " + sk.Name + " (" + shortPath(sk.Path) + ")")
			return m.submit(sk.Expand(strings.TrimPrefix(text, fields[0]))), true
		}
	}
	if cmd, ok := m.runMCPPrompt(fields[0], strings.TrimPrefix(text, fields[0])); ok {
		return cmd, true
	}
	return nil, false
}

// waitForTurn refuses a user command while a turn runs, and says so. A
// custom command, a skill and an MCP prompt each start a turn of their
// own, and one started beside the running turn ran two RunBlocks over
// one history and took the running turn's cancel away from esc.
func (m *model) waitForTurn(name string) bool {
	if !m.turn.running {
		return false
	}
	m.transcript.errorf("%s", m.waitsForTurn(name))
	m.refresh(false)
	return true
}

// waitsForTurn says an idle-only command must wait, naming the bound keys
// that cancel or steer the turn instead.
func (m *model) waitsForTurn(name string) string {
	msg := name + " waits for the turn to finish"
	if how := joinKept(", ", keyed(m.keys.First(keymap.Cancel), "%s cancels it"), keyed(m.keys.First(keymap.Steer), "%s steers it")); how != "" {
		msg += " (" + how + ")"
	}
	return msg
}

// /help alone is the keys-and-commands sheet it has always been.
// /help <topic> is a search over the feature catalogue and the
// manual compiled into the binary: no model call, no network.
func (m *model) cmdHelp(text string, fields []string) (tea.Cmd, bool) {
	if len(fields) == 2 && fields[1] == "all" {
		m.transcript.help(m.helpText())
		return nil, false
	}
	if len(fields) == 2 && isKeysQuery(fields[1]) {
		m.transcript.help(m.keysHelpText())
		return nil, false
	}
	if len(fields) > 1 && m.opts.Features != nil {
		m.transcript.info(feature.AnswerWidth(m.opts.Features, strings.Join(fields[1:], " "), "", m.width-4))
		return nil, false
	}
	m.transcript.help(m.briefHelpText())
	return nil, false
}

// isKeysQuery is /help keys and its spellings.
func isKeysQuery(w string) bool {
	switch strings.ToLower(w) {
	case "key", "keys", "keybinding", "keybindings", "shortcuts", "keyboard":
		return true
	}
	return false
}

func (m *model) cmdQuit(text string, fields []string) (tea.Cmd, bool) {
	return m.quit(), true
}

func (m *model) cmdClear(text string, fields []string) (tea.Cmd, bool) {
	sess, err := session.New(dirOf(m.opts.Session), m.opts.Cwd, m.opts.Provider, m.opts.Agent.Settings().Model)
	if err != nil {
		m.transcript.errorf("new session: %v", err)
		return nil, false
	}
	m.opts.Session = sess
	m.opts.CD.SetSession(sess)
	openErr := m.opts.Agent.OpenSession(sess, nil)
	m.titleGen++ // a title generated for the old conversation must not land here
	m.transcript = newTranscriptStyled(m.transcript.styleName, m.styles)
	m.transcript.cwd = m.opts.Cwd
	m.transcript.banner(m.welcome(""))
	// After the new transcript, not before: an error written into the
	// old one was thrown away with it.
	if openErr != nil {
		m.transcript.errorf("%v", openErr)
	}
	m.lastTurn, m.lastTurnTokens, m.lastVerdict = "", "", ""
	// After the reset of the status line, which used to blank it as soon
	// as it was set.
	m.note("new session " + sess.Meta.ID)
	m.pinned = true
	return nil, false
}

// modeRefused reports, and notes, a mode the managed policy will not let
// be turned on. Turning one off is always allowed.
func (m *model) modeRefused(mode string, on bool) bool {
	if !on {
		return false
	}
	if err := m.opts.Agent.ModeRefusal(mode); err != nil {
		m.transcript.errorf("%v", err)
		return true
	}
	return false
}

func (m *model) cmdYolo(text string, fields []string) (tea.Cmd, bool) {
	if m.modeRefused(managed.ModeYolo, !m.opts.Agent.Settings().AutoApprove) {
		return nil, false
	}
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) {
		s.AutoApprove = !s.AutoApprove
		if s.AutoApprove {
			s.AutoMode = false
		}
	})
	m.noteMode()
	return nil, false
}

// /routing vetoes the step router for the rest of the session: while it
// is off every model call goes to the session's own model, whatever
// route_cheap names. The veto is the trust control for issue #4's
// routing visibility — a route you do not trust stops at the next call,
// not at the config file.
func (m *model) cmdRouting(text string, fields []string) (tea.Cmd, bool) {
	if m.opts.Agent.ToggleOff("routing") {
		m.note("step routing off · every call goes to " + m.opts.Agent.Settings().Model + " (/routing again re-enables it)")
	} else {
		m.note("step routing on")
	}
	return nil, false
}

func (m *model) cmdAuto(text string, fields []string) (tea.Cmd, bool) {
	if m.modeRefused(managed.ModeAuto, !m.opts.Agent.Settings().AutoMode) {
		return nil, false
	}
	var on bool
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) {
		s.AutoMode = !s.AutoMode
		if on = s.AutoMode; on {
			s.AutoApprove = false
		}
	})
	if on {
		m.opts.Agent.SetPlanMode(false)
	}
	m.noteMode()
	return nil, false
}

func (m *model) cmdEdits(text string, fields []string) (tea.Cmd, bool) {
	if m.modeRefused(managed.ModeEdits, !m.opts.Agent.Settings().AutoEdits) {
		return nil, false
	}
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.AutoEdits = !s.AutoEdits })
	m.noteMode()
	return nil, false
}

func (m *model) cmdSettle(text string, fields []string) (tea.Cmd, bool) {
	if m.opts.Session == nil {
		m.transcript.errorf("no session to settle")
		return nil, false
	}
	settled, err := m.opts.Session.ToggleSettled()
	if err != nil {
		m.transcript.errorf("settle: %v", err)
	} else if settled {
		m.note("session settled · lists after the active ones in /resume")
	} else {
		m.note("session active again")
	}
	return nil, false
}

func (m *model) cmdReadonly(text string, fields []string) (tea.Cmd, bool) {
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) { s.ReadOnly = !s.ReadOnly })
	m.noteMode()
	return nil, false
}

func (m *model) cmdVerbose(text string, fields []string) (tea.Cmd, bool) {
	m.verbose = !m.verbose
	m.note("verbose tool output " + onOff(m.verbose))
	return nil, false
}

func (m *model) cmdVim(text string, fields []string) (tea.Cmd, bool) {
	m.vimCommandLine(strings.Join(fields[1:], " "))
	return nil, false
}

func (m *model) cmdModel(text string, fields []string) (tea.Cmd, bool) {
	// /model cheap|strong|advisor|vision picks one of the other slots
	// through the same dialog; anything else is a model id, as before.
	if cmd, open, handled := m.cmdModelSlot(fields); handled {
		return cmd, open
	}
	if len(fields) < 2 {
		if m.opts.Config == nil {
			m.note("model: " + m.opts.Agent.Settings().Model + " (" + m.opts.Provider + ")")
			return nil, false
		}
		return m.openConnect(stepModel), true
	}
	var prov llm.Provider
	if err := settingsapply.Allowed(m.opts.Config, "", "", fields[1]); err != nil {
		m.transcript.errorf("%v", err)
		return nil, false
	}
	if cfg := m.opts.Config; cfg != nil && m.opts.Models != nil {
		// The name may belong to a provider other than the one in
		// use; follow it rather than sending it to the wrong endpoint.
		if name, ok := cfg.RouteFor(fields[1]); ok && name != cfg.Provider {
			if err := settingsapply.Allowed(cfg, name, cfg.Providers[name].Type, ""); err != nil {
				m.transcript.errorf("%v", err)
				return nil, false
			}
			p, _, err := m.opts.Models(fields[1])
			if err != nil {
				m.transcript.errorf("%v", err)
				return nil, false
			}
			prov = p
			cfg.Provider = name
			m.opts.Provider = name
			m.note("switched to provider " + name + " for " + fields[1])
		}
	}
	settingsapply.SwitchModel(m.opts.Config, m.opts.Agent, fields[1], prov)
	if cfg := m.opts.Config; cfg != nil {
		cfg.Model = fields[1]
		if m.opts.ConfigPath != "" {
			if err := config.Update(m.opts.ConfigPath, cfg.Provider, cfg.Active(), fields[1]); err != nil {
				m.transcript.errorf("saving config: %v", err)
			}
		}
	}
	m.note("model set to " + fields[1] + " on " + m.opts.Provider)
	return nil, false
}

func (m *model) cmdPermissions(text string, fields []string) (tea.Cmd, bool) {
	m.permissionsCommand(fields[1:])
	return nil, false
}

func (m *model) cmdReload(text string, fields []string) (tea.Cmd, bool) {
	m.opts.Agent.RefreshExtra()
	m.note("memory and skill indexes will be rebuilt for the next call")
	return nil, false
}

func (m *model) cmdContext(text string, fields []string) (tea.Cmd, bool) {
	m.contextCommand()
	return nil, false
}

func (m *model) cmdTodos(text string, fields []string) (tea.Cmd, bool) {
	m.openOverlay(overlayTodos)
	return nil, false
}

func (m *model) cmdInspect(text string, fields []string) (tea.Cmd, bool) {
	m.inspectCommand(cmdArg(text, fields))
	return nil, false
}

// cmdInit writes the project's starter instruction file. It never
// overwrites one without "force": the existing file is the user's.
func (m *model) cmdInit(text string, fields []string) (tea.Cmd, bool) {
	force := strings.EqualFold(strings.TrimSpace(cmdArg(text, fields)), "force")
	m.showResult(projectinit.Run(m.opts.Cwd, force))
	return nil, false
}

func (m *model) cmdSkills(text string, fields []string) (tea.Cmd, bool) {
	where := "add .janetcode/skills/<name>/SKILL.md (or <name>.md) to the project, or skills/ under " + shortPath(m.opts.ConfigDir) + "; .claude/skills is read too"
	m.showResult(skill.Report(skill.WithBuiltins(skill.Load(m.opts.Cwd, m.opts.ConfigDir)), m.opts.Skills.Stats(), m.prefixTokens(), where), nil)
	return nil, false
}

// prefixTokens estimates the part of the request that is the same every
// turn — system prompt, project context, pins and tool schemas — which
// is what a skill's index line is a share of.
func (m *model) prefixTokens() int {
	if m.opts.Agent == nil {
		return 0
	}
	c := m.opts.Agent.Context()
	return c.System + c.Extra + c.Pins + c.Tools
}

func (m *model) cmdRules(text string, fields []string) (tea.Cmd, bool) {
	rs := rules.Load(m.opts.Cwd, m.opts.ConfigDir)
	if len(rs) == 0 {
		m.transcript.info("no path-scoped rules: add .janetcode/rules/<name>.md with a paths: list in its frontmatter, and it is sent once, when a tool call touches a matching path")
		return nil, false
	}
	var sb strings.Builder
	for _, r := range rs {
		state := "waiting"
		if m.opts.Rules.Sent(r.Name) {
			state = "sent"
		}
		fmt.Fprintf(&sb, "%-18s %-7s %-26s %s\n", r.Name, state, strings.Join(r.Paths, ","), r.Description)
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	return nil, false
}

func (m *model) cmdMemory(text string, fields []string) (tea.Cmd, bool) {
	if fields[0] == "/dream" {
		return m.dreamMemory(), true
	}
	if fields[0] == "/sleep" {
		return m.sleepMemory(true), true
	}
	m.memoryCommand(fields[0], cmdArg(text, fields))
	return nil, false
}

func (m *model) cmdUndo(text string, fields []string) (tea.Cmd, bool) {
	if fields[0] == "/redo" {
		m.undoCommand(fields[0], false)
		return nil, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	ua, err := agent.ParseUndoArgs(rest)
	if err != nil {
		m.transcript.errorf("%v", err)
		return nil, false
	}
	switch {
	case ua.Apply:
		return m.undoApplyCommand(ua.Force), true
	case ua.Turn != 0:
		return m.undoTurnCommand(ua.Turn, ua.FilesOnly), true
	case ua.About:
		if ua.Description == "" {
			m.transcript.errorf("usage: /undo about <description>")
			break
		}
		return m.undoAboutCommand(ua.Description), true
	case ua.N != 0:
		m.undoChange(ua.N)
	default:
		m.undoCommand(fields[0], ua.Preview)
	}
	return nil, false
}

func (m *model) cmdFind(text string, fields []string) (tea.Cmd, bool) {
	q := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if q == "" {
		q = m.finder.query
	}
	if q == "" {
		m.transcript.errorf("usage: /find <text>")
		return nil, false
	}
	return m.find(q), true
}

func (m *model) cmdResume(text string, fields []string) (tea.Cmd, bool) {
	if len(fields) < 2 {
		return m.openSessionPicker(), true
	}
	m.resume(strings.Join(fields[1:], " "))
	return nil, false
}

// cmdRecover carries on the turn the session died in (see
// docs/notes/durable-turns.md). It runs like a turn the user started:
// same spinner, same esc, same verdict.
func (m *model) cmdRecover(text string, fields []string) (tea.Cmd, bool) {
	if m.turn.running {
		m.transcript.errorf("a turn is already running")
		return nil, false
	}
	if m.opts.Agent.Interrupted() == nil {
		m.transcript.info("nothing to recover: the last turn ended")
		return nil, false
	}
	if m.opts.Agent.Settings().Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect")
		return nil, false
	}
	m.startTurn("thinking")
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	ag, send, queue := m.opts.Agent, m.send, m.queue
	m.refresh(false)
	go func() {
		send(agentDoneMsg{err: ag.ContinueInterrupted(ctx, queue.push)})
	}()
	return m.spinner.Tick, true
}

// noteInterrupted tells the user, on opening a session, that its last
// turn died and how to carry it on.
func (m *model) noteInterrupted() {
	if n := m.opts.Agent.InterruptedNotice("/recover"); n != "" {
		m.transcript.info(n)
	}
}

func (m *model) cmdCompact(text string, fields []string) (tea.Cmd, bool) {
	if m.turn.running {
		m.transcript.errorf("wait for the current turn to finish before compacting")
		return nil, false
	}
	// The summary is a model call. Handed no provider it dereferenced
	// nil on the compaction goroutine and the whole program died.
	if m.opts.Agent.Settings().Provider == nil {
		m.transcript.errorf("not connected to a provider yet: run /connect (/compact-fast prunes without one)")
		return nil, false
	}
	return m.compact(), true
}

func (m *model) cmdCompactFast(text string, fields []string) (tea.Cmd, bool) {
	if m.turn.running {
		m.transcript.errorf("wait for the current turn to finish before compacting")
		return nil, false
	}
	return m.compactFast(), true
}

func (m *model) cmdCurator(text string, fields []string) (tea.Cmd, bool) {
	var stats map[string]skill.Stat
	if m.opts.Skills != nil {
		stats = m.opts.Skills.Stats()
	}
	out, err := skill.Curate(m.opts.Cwd, stats, cmdArg(text, fields))
	if err != nil {
		m.transcript.errorf("%v", err)
		return nil, false
	}
	m.transcript.info(out)
	return nil, false
}

func (m *model) cmdExport(text string, fields []string) (tea.Cmd, bool) {
	path := ""
	if len(fields) > 1 {
		path = strings.Join(fields[1:], " ")
	}
	m.export(path)
	return nil, false
}

func (m *model) cmdUsage(text string, fields []string) (tea.Cmd, bool) {
	u := m.opts.Agent.UsageSnapshot()
	line := "session tokens · " + u.CacheLine() + " · context now ~" + textfmt.Tokens(m.tokensInUse()) +
		"\n" + m.opts.Agent.CostNote()
	// Which model spent it: a session routes, falls back, asks an advisor
	// and delegates to subagents, and the total alone cannot say where it
	// went.
	if s := agent.FormatUsageByModel(m.opts.Agent.UsageByModel(), m.opts.Agent.PriceOf); s != "" {
		line += "\n" + s
	}
	m.transcript.info(line)
	return nil, false
}

func (m *model) cmdFeatures(text string, fields []string) (tea.Cmd, bool) {
	m.featuresCommand(fields[1:])
	return nil, false
}

func (m *model) cmdJev(text string, fields []string) (tea.Cmd, bool) {
	cmd := m.jevCommand(fields[1:])
	return cmd, cmd != nil
}
