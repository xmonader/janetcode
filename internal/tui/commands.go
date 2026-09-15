package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/settingsapply"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
)

type command struct {
	name, args, help string
	// argsFor lists the completions for this command's argument, when
	// they can be enumerated.
	argsFor func(m *model, partial string) []command
}

// argCompleters lists the commands whose argument can be enumerated.
var argCompleters = map[string]func(m *model, partial string) []command{
	"/resume": (*model).completeSession,
	"/model":  (*model).completeModel,
	"/theme":  (*model).completeTheme,
	"/forget": (*model).completeMemory,
	"/undo":   (*model).completeUndo,
	"/open":   (*model).completeFile,
	"/diff":   (*model).completeFile,
	"/agents": (*model).completeAgents,
	"/jobs":   (*model).completeJobs,
}

// commands is the shared slash table with the terminal's completions.
var commands = func() []command {
	out := make([]command, 0, len(slash.Commands))
	for _, c := range slash.Commands {
		out = append(out, command{name: c.Name, args: c.Args, help: c.Help, argsFor: argCompleters[c.Name]})
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
	"/yolo": (*model).cmdYolo,
	"/auto": (*model).cmdAuto,
	"/sandbox": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.sandboxCommand(cmdArg(text, fields))
		return nil, false
	},
	"/review": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.reviewCommand(cmdArg(text, fields)), true
	},
	"/loop": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.loopCommand(cmdArg(text, fields))
		return nil, false
	},
	"/edits":    (*model).cmdEdits,
	"/settle":   (*model).cmdSettle,
	"/readonly": (*model).cmdReadonly,
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
	"/verbose": (*model).cmdVerbose,
	"/connect": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.openConnect(stepPick), true
	},
	"/model": (*model).cmdModel,
	"/mcp": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.mcpCommand(fields[1:])
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
	"/skills":   (*model).cmdSkills,
	"/rules":    (*model).cmdRules,
	"/memory":   (*model).cmdMemory,
	"/remember": (*model).cmdMemory,
	"/forget":   (*model).cmdMemory,
	"/undo":     (*model).cmdUndo,
	"/redo":     (*model).cmdUndo,
	"/changes": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.changesCommand(fields[1:])
		return nil, false
	},
	"/blame": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.blameCommand(cmdArg(text, fields))
		return nil, false
	},
	"/find": (*model).cmdFind, "/search": (*model).cmdFind,
	"/resume": (*model).cmdResume, "/sessions": (*model).cmdResume,
	"/compact": (*model).cmdCompact,
	"/export":  (*model).cmdExport,
	"/stats": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.statsCommand()
		return nil, false
	},
	"/usage": (*model).cmdUsage, "/cost": (*model).cmdUsage,
	"/budget": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		m.budgetCommand(cmdArg(text, fields))
		return nil, false
	},
	"/eval": func(m *model, text string, fields []string) (tea.Cmd, bool) {
		return m.evalCommand(cmdArg(text, fields)), true
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
	fields := strings.Fields(text)
	// Idle-only is decided by the slash table alone. Only table names are
	// gated: an alias resolves to its handler below without this check.
	if m.running {
		if c, ok := slash.Lookup(fields[0]); ok && c.Idle {
			m.transcript.errorf("%s waits for the turn to finish (esc cancels it, ctrl+s steers it)", fields[0])
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
		m.transcript.errorf("unknown command %s (try /help)", fields[0])
	}
	m.refresh(false)
	return nil
}

// runUserCommand runs a name the built-in table does not know: a custom
// command, a user-invocable skill, or an MCP prompt, in that order.
func (m *model) runUserCommand(text string, fields []string) (tea.Cmd, bool) {
	if c, ok := m.custom[fields[0]]; ok {
		args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		return m.submit(c.Expand(args)), true
	}
	for _, sk := range skill.Load(m.opts.Cwd, m.opts.ConfigDir) {
		if sk.UserInvocable && "/"+sk.Name == fields[0] {
			m.note("running skill " + sk.Name + " (" + shortPath(sk.Path) + ")")
			return m.submit(sk.Expand(strings.TrimPrefix(text, fields[0]))), true
		}
	}
	if cmd, ok := m.runMCPPrompt(fields[0], strings.TrimPrefix(text, fields[0])); ok {
		return cmd, true
	}
	return nil, false
}

// /help alone is the keys-and-commands sheet it has always been.
// /help <topic> is a search over the feature catalogue and the
// manual compiled into the binary: no model call, no network.
func (m *model) cmdHelp(text string, fields []string) (tea.Cmd, bool) {
	if len(fields) > 1 && m.opts.Features != nil {
		m.transcript.info(feature.AnswerWidth(m.opts.Features, strings.Join(fields[1:], " "), "", m.width-4))
		return nil, false
	}
	m.transcript.info(m.helpText())
	return nil, false
}

func (m *model) cmdQuit(text string, fields []string) (tea.Cmd, bool) {
	m.quitting = true
	return tea.Quit, true
}

func (m *model) cmdClear(text string, fields []string) (tea.Cmd, bool) {
	sess, err := session.New(dirOf(m.opts.Session), m.opts.Cwd, m.opts.Provider, m.opts.Agent.Settings().Model)
	if err != nil {
		m.transcript.errorf("new session: %v", err)
		return nil, false
	}
	m.opts.Session = sess
	if err := m.opts.Agent.OpenSession(sess, nil); err != nil {
		m.transcript.errorf("%v", err)
	}
	m.titleGen++ // a title generated for the old conversation must not land here
	m.transcript = newTranscriptStyled(m.transcript.styleName, m.styles)
	m.transcript.cwd = m.opts.Cwd
	m.transcript.banner(m.welcome(""))
	m.note("new session " + sess.Meta.ID)
	m.lastTurn, m.lastVerdict, m.status = "", "", ""
	m.pinned = true
	return nil, false
}

func (m *model) cmdYolo(text string, fields []string) (tea.Cmd, bool) {
	m.opts.Agent.UpdateSettings(func(s *agent.Settings) {
		s.AutoApprove = !s.AutoApprove
		if s.AutoApprove {
			s.AutoMode = false
		}
	})
	m.noteMode()
	return nil, false
}

func (m *model) cmdAuto(text string, fields []string) (tea.Cmd, bool) {
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

func (m *model) cmdModel(text string, fields []string) (tea.Cmd, bool) {
	if len(fields) < 2 {
		if m.opts.Config == nil {
			m.note("model: " + m.opts.Agent.Settings().Model + " (" + m.opts.Provider + ")")
			return nil, false
		}
		return m.openConnect(stepModel), true
	}
	var prov llm.Provider
	if cfg := m.opts.Config; cfg != nil && m.opts.Models != nil {
		// The name may belong to a provider other than the one in
		// use; follow it rather than sending it to the wrong endpoint.
		if name, ok := cfg.RouteFor(fields[1]); ok && name != cfg.Provider {
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

func (m *model) cmdSkills(text string, fields []string) (tea.Cmd, bool) {
	skills := skill.Load(m.opts.Cwd, m.opts.ConfigDir)
	if len(skills) == 0 {
		m.transcript.info("no skills found: add .janetcode/skills/<name>/SKILL.md (or <name>.md) to the project, or skills/ under " + shortPath(m.opts.ConfigDir) + "; .claude/skills is read too")
		return nil, false
	}
	var sb strings.Builder
	for _, s := range skills {
		fmt.Fprintf(&sb, "%-20s %s  (%s)\n", s.Name, s.Description, s.Source)
	}
	m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	return nil, false
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
		q = m.lastFind
	}
	if q == "" {
		m.transcript.errorf("usage: /find <text>")
		return nil, false
	}
	m.find(q)
	return nil, true
}

func (m *model) cmdResume(text string, fields []string) (tea.Cmd, bool) {
	if len(fields) < 2 {
		return m.openSessionPicker(), true
	}
	m.resume(fields[1])
	return nil, false
}

func (m *model) cmdCompact(text string, fields []string) (tea.Cmd, bool) {
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before compacting")
		return nil, false
	}
	return m.compact(), true
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
	line := "session tokens · " + u.TokenLine() + " · context now ~" + textfmt.Tokens(m.opts.Agent.EstimateTokens()) +
		"\n" + u.CostNote(m.opts.Agent.Settings().Price)
	m.transcript.info(line)
	return nil, false
}
