package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/feature"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// availability says when a command can run. Resolving an idle-only
// command while a turn is running returns a blocked message instead of
// each command checking m.running for itself and saying it differently.
type availability int

const (
	availAlways availability = iota
	availIdle                // needs the turn to finish first
)

type command struct {
	name, args, help string
	avail            availability
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
		cmd := command{name: c.Name, args: c.Args, help: c.Help, argsFor: argCompleters[c.Name]}
		if c.Idle {
			cmd.avail = availIdle
		}
		out = append(out, cmd)
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

func (m *model) command(text string) tea.Cmd {
	fields := strings.Fields(text)
	if m.running {
		if c, ok := lookup(fields[0]); ok && c.avail == availIdle {
			m.transcript.errorf("%s waits for the turn to finish (esc cancels it, ctrl+s steers it)", fields[0])
			m.refresh(false)
			return nil
		}
	}
	m.noteUse(fields[0])
	switch fields[0] {
	case "/help", "/?":
		// /help alone is the keys-and-commands sheet it has always been.
		// /help <topic> is a search over the feature catalogue and the
		// manual compiled into the binary: no model call, no network.
		if len(fields) > 1 && m.opts.Features != nil {
			m.transcript.info(feature.AnswerWidth(m.opts.Features, strings.Join(fields[1:], " "), "", m.width-4))
			break
		}
		m.transcript.info(m.helpText())
	case "/quit", "/exit", "/q":
		m.quitting = true
		return tea.Quit
	case "/clear", "/new":
		sess, err := session.New(dirOf(m.opts.Session), m.opts.Cwd, m.opts.Provider, m.opts.Agent.Model)
		if err != nil {
			m.transcript.errorf("new session: %v", err)
			break
		}
		m.opts.Session = sess
		if err := m.opts.Agent.OpenSession(sess, nil); err != nil {
			m.transcript.errorf("%v", err)
		}
		m.titleGen++ // a title generated for the old conversation must not land here
		m.transcript = newTranscriptStyled(m.transcript.styleName)
		m.transcript.cwd = m.opts.Cwd
		m.transcript.banner(m.welcome(""))
		m.note("new session " + sess.Meta.ID)
		m.lastTurn, m.lastVerdict, m.status = "", "", ""
		m.pinned = true
	case "/yolo":
		m.opts.Agent.AutoApprove = !m.opts.Agent.AutoApprove
		if m.opts.Agent.AutoApprove {
			m.opts.Agent.AutoMode = false
		}
		m.noteMode()
	case "/auto":
		m.opts.Agent.AutoMode = !m.opts.Agent.AutoMode
		if m.opts.Agent.AutoMode {
			m.opts.Agent.AutoApprove = false
			m.opts.Agent.SetPlanMode(false)
		}
		m.noteMode()
	case "/sandbox":
		m.sandboxCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/review":
		return m.reviewCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/loop":
		m.loopCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/edits":
		m.opts.Agent.AutoEdits = !m.opts.Agent.AutoEdits
		m.noteMode()
	case "/settle":
		if m.opts.Session == nil {
			m.transcript.errorf("no session to settle")
			break
		}
		settled, err := m.opts.Session.ToggleSettled()
		if err != nil {
			m.transcript.errorf("settle: %v", err)
		} else if settled {
			m.note("session settled · lists after the active ones in /resume")
		} else {
			m.note("session active again")
		}
	case "/readonly":
		m.opts.Agent.ReadOnly = !m.opts.Agent.ReadOnly
		m.noteMode()
	case "/plan":
		return m.planCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/why":
		m.whyCommand(fields[1:])
	case "/trajectory":
		m.trajectoryCommand(fields[1:])
	case "/feedback":
		m.feedbackCommand(fields[1:])
	case "/goal":
		return m.goalCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/agents":
		m.agentsCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/ssh":
		m.sshCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/docker":
		m.dockerCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/theme":
		if cmd := m.themeCommand(fields[1:]); cmd != nil {
			return cmd
		}
	case "/hints":
		m.hintsCommand(fields[1:])
	case "/verbose":
		m.verbose = !m.verbose
		m.note("verbose tool output " + onOff(m.verbose))
	case "/connect":
		return m.openConnect(stepPick)
	case "/model":
		if len(fields) < 2 {
			if m.opts.Config == nil {
				m.note("model: " + m.opts.Agent.Model + " (" + m.opts.Provider + ")")
				break
			}
			return m.openConnect(stepModel)
		}
		if cfg := m.opts.Config; cfg != nil && m.opts.Models != nil {
			// The name may belong to a provider other than the one in
			// use; follow it rather than sending it to the wrong endpoint.
			if name, ok := cfg.RouteFor(fields[1]); ok && name != cfg.Provider {
				prov, _, err := m.opts.Models(fields[1])
				if err != nil {
					m.transcript.errorf("%v", err)
					break
				}
				m.opts.Agent.Provider = prov
				cfg.Provider = name
				m.opts.Provider = name
				m.note("switched to provider " + name + " for " + fields[1])
			}
		}
		m.opts.Agent.Model = fields[1]
		m.applyModelMeta(fields[1])
		if cfg := m.opts.Config; cfg != nil {
			cfg.Model = fields[1]
			if m.opts.ConfigPath != "" {
				if err := config.Update(m.opts.ConfigPath, cfg.Provider, cfg.Active(), fields[1]); err != nil {
					m.transcript.errorf("saving config: %v", err)
				}
			}
		}
		m.note("model set to " + fields[1] + " on " + m.opts.Provider)
	case "/mcp":
		m.mcpCommand(fields[1:])
	case "/hooks":
		m.hooksCommand(fields[1:])
	case "/permissions", "/perms":
		m.permissionsCommand(fields[1:])
	case "/lsp":
		m.lspCommand()
	case "/jobs":
		m.jobsCommand(fields[1:])
	case "/open":
		m.openCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/diff":
		m.diffCommand(fields[1:])
	case "/fork":
		m.forkSession(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/add-dir":
		m.addDirCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/btw":
		return m.btwCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/copy":
		m.copyCommand()
	case "/config":
		m.configCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/doctor":
		return m.doctorCommand()
	case "/title":
		m.setTitle(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/history":
		m.searchSessions(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/reload":
		m.opts.Agent.RefreshExtra()
		m.note("memory and skill indexes will be rebuilt for the next call")
	case "/context", "/ctx":
		m.contextCommand()
	case "/todos", "/todo":
		m.openOverlay(overlayTodos)
	case "/inspect", "/i":
		m.inspectCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/pin":
		m.pinCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/unpin":
		m.unpinCommand(fields[1:])
	case "/verify":
		if cmd := m.verifyCommand(fields[1:]); cmd != nil {
			return cmd
		}
	case "/skills":
		skills := skill.Load(m.opts.Cwd, m.opts.ConfigDir)
		if len(skills) == 0 {
			m.transcript.info("no skills found: add .janetcode/skills/<name>/SKILL.md (or <name>.md) to the project, or skills/ under " + shortPath(m.opts.ConfigDir) + "; .claude/skills is read too")
			break
		}
		var sb strings.Builder
		for _, s := range skills {
			fmt.Fprintf(&sb, "%-20s %s  (%s)\n", s.Name, s.Description, s.Source)
		}
		m.transcript.info(strings.TrimRight(sb.String(), "\n"))
	case "/rules":
		rs := rules.Load(m.opts.Cwd, m.opts.ConfigDir)
		if len(rs) == 0 {
			m.transcript.info("no path-scoped rules: add .janetcode/rules/<name>.md with a paths: list in its frontmatter, and it is sent once, when a tool call touches a matching path")
			break
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
	case "/memory", "/remember", "/forget":
		m.memoryCommand(fields[0], strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/undo", "/redo":
		if n, err := strconv.Atoi(strings.Join(fields[1:], "")); err == nil && fields[0] == "/undo" {
			m.undoChange(n)
		} else {
			m.undoCommand(fields[0], len(fields) > 1 && fields[1] == "preview")
		}
	case "/changes":
		m.changesCommand(fields[1:])
	case "/blame":
		m.blameCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/find", "/search":
		q := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		if q == "" {
			q = m.lastFind
		}
		if q == "" {
			m.transcript.errorf("usage: /find <text>")
			break
		}
		m.find(q)
		return nil
	case "/resume", "/sessions":
		if len(fields) < 2 {
			return m.openSessionPicker()
		}
		m.resume(fields[1])
	case "/compact":
		if m.running {
			m.transcript.errorf("wait for the current turn to finish before compacting")
			break
		}
		return m.compact()
	case "/export":
		path := ""
		if len(fields) > 1 {
			path = strings.Join(fields[1:], " ")
		}
		m.export(path)
	case "/stats":
		m.statsCommand()
	case "/usage", "/cost":
		u := m.opts.Agent.UsageSnapshot()
		line := "session tokens · " + u.TokenLine() + " · context now ~" + textfmt.Tokens(m.opts.Agent.EstimateTokens()) +
			"\n" + u.CostNote(m.opts.Agent.Price)
		m.transcript.info(line)
	case "/budget":
		m.budgetCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	case "/eval":
		m.evalCommand(strings.TrimSpace(strings.TrimPrefix(text, fields[0])))
	default:
		if c, ok := m.custom[fields[0]]; ok {
			args := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
			return m.submit(c.Expand(args))
		}
		for _, sk := range skill.Load(m.opts.Cwd, m.opts.ConfigDir) {
			if sk.UserInvocable && "/"+sk.Name == fields[0] {
				m.note("running skill " + sk.Name + " (" + shortPath(sk.Path) + ")")
				return m.submit(sk.Expand(strings.TrimPrefix(text, fields[0])))
			}
		}
		if cmd, ok := m.runMCPPrompt(fields[0], strings.TrimPrefix(text, fields[0])); ok {
			return cmd
		}
		m.transcript.errorf("unknown command %s (try /help)", fields[0])
	}
	m.refresh(false)
	return nil
}
