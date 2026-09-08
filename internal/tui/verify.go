package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/verify"
)

// VerifyOptions carries the project's verification commands and whether
// they may run without the user having approved the file.
type VerifyOptions struct {
	Config         verify.Config
	ProjectHash    string
	ProjectTrusted bool
	Trust          mcp.TrustStore
}

// verifyDoneMsg reports a manual /verify run.
type verifyDoneMsg struct{ res verify.Result }

// verifyCommand implements /verify [set <cmd>|clear|trust].
func (m *model) verifyCommand(args []string) tea.Cmd {
	o := m.opts.Verify
	if o == nil {
		o = &VerifyOptions{}
		m.opts.Verify = o
	}
	ag := m.opts.Agent
	if len(args) > 0 {
		switch args[0] {
		case "set":
			cmd := strings.TrimSpace(strings.Join(args[1:], " "))
			if cmd == "" {
				m.transcript.errorf("usage: /verify set <command>")
				return nil
			}
			c := o.Config
			c.Commands = append(append([]string(nil), c.Commands...), cmd)
			return m.saveVerify(c, "added: "+cmd)
		case "mutate":
			cmd := strings.TrimSpace(strings.Join(args[1:], " "))
			c := o.Config
			if cmd == "" || cmd == "off" {
				c.Mutate = ""
				return m.saveVerify(c, "mutation testing off")
			}
			c.Mutate = cmd
			return m.saveVerify(c, "mutation testing with: "+cmd+" (small mutations of changed lines must make it fail)")
		case "clear":
			return m.saveVerify(verify.Config{}, "cleared "+verify.ProjectFile)
		case "trust":
			switch {
			case len(o.Config.Commands) == 0:
				m.transcript.errorf("no %s in %s", verify.ProjectFile, shortPath(m.opts.Cwd))
			case o.ProjectTrusted:
				m.transcript.info("project verification is already trusted")
			default:
				if err := o.Trust.Trust(m.opts.Cwd+"#verify", o.ProjectHash); err != nil {
					m.transcript.errorf("record trust: %v", err)
					return nil
				}
				o.ProjectTrusted = true
				ag.Verify = &verify.Runner{Config: o.Config, Cwd: m.opts.Cwd}
				m.transcript.info("trusted " + verify.ProjectFile + " · its checks run after every change")
			}
			return nil
		default:
			m.transcript.errorf("usage: /verify [set <command>|mutate <test command>|mutate off|clear|trust]")
			return nil
		}
	}
	if len(o.Config.Commands) == 0 && o.Config.Mutate == "" {
		m.transcript.info("no verification commands: /verify set \"make test\" writes " + verify.ProjectFile + "; its commands run after every turn that changes something")
		return nil
	}
	if !o.ProjectTrusted {
		m.transcript.errorf("%s is not trusted yet: review it, then run /verify trust", verify.ProjectFile)
		return nil
	}
	if m.running {
		m.transcript.errorf("wait for the current turn to finish before verifying")
		return nil
	}
	m.startTurn("verifying")
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	r, send := &verify.Runner{Config: o.Config, Cwd: m.opts.Cwd}, m.send
	m.transcript.info("verifying: " + strings.Join(o.Config.Commands, " · "))
	m.refresh(false)
	go func() { send(verifyDoneMsg{res: r.Run(ctx)}) }()
	return m.spinner.Tick
}

// saveVerify writes the project file. The user wrote it, so it is trusted.
func (m *model) saveVerify(c verify.Config, done string) tea.Cmd {
	o := m.opts.Verify
	if err := verify.Save(m.opts.Cwd, c); err != nil {
		m.transcript.errorf("write %s: %v", verify.ProjectFile, err)
		return nil
	}
	cfg, hash, err := verify.Load(m.opts.Cwd)
	if err != nil {
		m.transcript.errorf("%v", err)
		return nil
	}
	o.Config, o.ProjectHash, o.ProjectTrusted = cfg, hash, true
	if o.Trust.Path != "" && hash != "" {
		if err := o.Trust.Trust(m.opts.Cwd+"#verify", hash); err != nil {
			m.transcript.errorf("record trust: %v", err)
		}
	}
	if len(cfg.Commands) == 0 {
		m.opts.Agent.Verify = nil
	} else {
		m.opts.Agent.Verify = &verify.Runner{Config: cfg, Cwd: m.opts.Cwd}
	}
	m.transcript.info(done + " · " + verifyLine(cfg))
	return nil
}

func verifyLine(c verify.Config) string {
	if len(c.Commands) == 0 {
		return "no checks"
	}
	return "checks: " + strings.Join(c.Commands, " · ")
}

// finishVerify reports a manual run.
func (m *model) finishVerify(msg verifyDoneMsg) {
	m.running = false
	m.phase = ""
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if msg.res.OK() {
		m.transcript.info("✓ " + msg.res.Summary())
		m.status = "verified"
		return
	}
	m.transcript.errorf("%s\n%s", msg.res.Summary(), strings.TrimSpace(msg.res.Output))
	m.status = "verification failed"
}
