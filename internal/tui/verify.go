package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/mutate"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/verify"
)

// VerifyOptions carries the project's verification commands and whether
// they may run without the user having approved the file.
type VerifyOptions struct {
	Config         verify.Config
	ProjectHash    string
	ProjectTrusted bool
	Trust          trust.Store
}

// verifyDoneMsg reports a manual /verify run.
type verifyDoneMsg struct{ res verify.Result }

// verifyCommand implements /verify [set <cmd>|add <cmd>|mutate <cmd>|clear|trust];
// the edits to the project file are verify.Edit's.
func (m *model) verifyCommand(args []string) tea.Cmd {
	o := m.opts.Verify
	if o == nil {
		o = &VerifyOptions{}
		m.opts.Verify = o
	}
	// The config is loaded once at startup (see cmd/janetcode/main.go), so
	// a project file that was missing or malformed then, and got written
	// or fixed since, would otherwise go unnoticed until the process
	// restarts: every /verify and /verify trust re-reads it from disk
	// first, the same way saveVerify already does after a /verify set.
	if err := m.reloadVerify(o); err != nil {
		m.transcript.errorf("%v", err)
		return nil
	}
	ag := m.opts.Agent
	if len(args) > 0 {
		switch args[0] {
		case "set", "add", "mutate", "clear":
			c, done, err := verify.Edit(o.Config, args[0], strings.Join(args[1:], " "))
			if err != nil {
				m.transcript.errorf("%v", err)
				return nil
			}
			return m.saveVerify(c, done)
		case "trust":
			switch {
			case len(o.Config.Commands) == 0:
				m.transcript.errorf("no %s in %s", verify.ProjectFile, shortPath(m.opts.Cwd))
			case o.ProjectTrusted:
				m.note("project verification is already trusted")
			case m.execTrustBlocked():
			default:
				if err := o.Trust.Trust(trust.VerifyKey(m.opts.Cwd), o.ProjectHash); err != nil {
					m.transcript.errorf("record trust: %v", err)
					return nil
				}
				o.ProjectTrusted = true
				ag.SetVerify(&verify.Runner{Config: o.Config, Cwd: m.opts.Cwd, Mutate: mutate.ForVerify}, false)
				m.note("trusted " + verify.ProjectFile + " · its checks run after every change")
			}
			return nil
		default:
			m.transcript.errorf("%s", verify.Usage)
			return nil
		}
	}
	if len(o.Config.Commands) == 0 && o.Config.Mutate == "" {
		m.transcript.info("no verification commands: /verify set make test writes " + verify.ProjectFile + "; its commands run after every turn that changes something")
		return nil
	}
	if !o.ProjectTrusted {
		m.transcript.errorf("%s is not trusted yet: review it, then run /verify trust", verify.ProjectFile)
		return nil
	}
	if m.turn.running {
		m.transcript.errorf("wait for the current turn to finish before verifying")
		return nil
	}
	m.startTurn("verifying")
	ctx, cancel := context.WithCancel(m.life)
	m.turn.cancel = cancel
	r, send := &verify.Runner{Config: o.Config, Cwd: m.opts.Cwd, Mutate: mutate.ForVerify}, m.send
	m.note("verifying: " + strings.Join(o.Config.Commands, " · "))
	m.refresh(false)
	go func() { send(verifyDoneMsg{res: r.Run(ctx)}) }()
	return m.spinner.Tick
}

// reloadVerify re-reads the project's verify config from disk into o,
// recomputing whether it is trusted from the file's current content hash
// (trust itself is still keyed by that hash, via o.Trust, so fixing a
// malformed file does not silently inherit trust an earlier, different
// content never earned) and keeping the agent's own Verify/VerifyUntrusted
// in step with it. A malformed file is reported to the caller and leaves
// o and the agent exactly as they were, the same as saveVerify already
// does when a write leaves the file unparsable.
func (m *model) reloadVerify(o *VerifyOptions) error {
	cfg, hash, err := verify.Load(m.opts.Cwd)
	if err != nil {
		return err
	}
	o.Config, o.ProjectHash = cfg, hash
	defined := len(cfg.Commands) > 0 || cfg.Mutate != ""
	o.ProjectTrusted = defined && o.Trust.Trusted(trust.VerifyKey(m.opts.Cwd), hash)
	ag := m.opts.Agent
	if o.ProjectTrusted {
		ag.SetVerify(&verify.Runner{Config: cfg, Cwd: m.opts.Cwd, Mutate: mutate.ForVerify}, false)
	} else {
		ag.SetVerify(nil, defined)
	}
	return nil
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
		if err := o.Trust.Trust(trust.VerifyKey(m.opts.Cwd), hash); err != nil {
			m.transcript.errorf("record trust: %v", err)
		}
	}
	_, untrusted := m.opts.Agent.VerifyGate()
	if len(cfg.Commands) == 0 {
		m.opts.Agent.SetVerify(nil, untrusted)
	} else {
		m.opts.Agent.SetVerify(&verify.Runner{Config: cfg, Cwd: m.opts.Cwd, Mutate: mutate.ForVerify}, untrusted)
	}
	m.note(done + " · " + verifyLine(cfg))
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
	m.turn.running = false
	m.turn.phase = ""
	m.turn.release()
	if msg.res.Mutation != nil && msg.res.Mutation.Scored() {
		m.opts.Agent.LogEvent(msg.res.Mutation.Event())
	}
	if msg.res.OK() {
		m.note("✓ " + msg.res.Summary())
		return
	}
	m.transcript.errorf("%s\n%s", msg.res.Summary(), strings.TrimSpace(msg.res.Output))
	m.status = "verification failed"
}
