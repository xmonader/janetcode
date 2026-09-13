package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

func (s *server) model(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		s.notice("model: " + ag.Model + " (" + s.opts.Provider + ") · /model <name> switches, across providers when the name routes elsewhere")
		return
	}
	cfg := s.opts.Config
	if cfg != nil && s.opts.Models != nil {
		// The name may belong to a provider other than the one in use;
		// follow it rather than sending it to the wrong endpoint.
		if name, ok := cfg.RouteFor(arg); ok && name != cfg.Provider {
			prov, _, err := s.opts.Models(arg)
			if err != nil {
				s.errf("%v", err)
				return
			}
			ag.Provider = prov
			cfg.Provider = name
			s.opts.Provider = name
			s.notice("switched to provider " + name + " for " + arg)
		}
	}
	ag.Model = arg
	if cfg != nil {
		ag.Price = cfg.PriceFor(arg)
		ag.ContextWindow = cfg.MetaFor(arg).ContextWindow
		cfg.Model = arg
		if s.opts.ConfigPath != "" {
			if err := config.Update(s.opts.ConfigPath, cfg.Provider, cfg.Active(), arg); err != nil {
				s.errf("saving config: %v", err)
			}
		}
	}
	s.notice("model set to " + arg + " on " + s.opts.Provider)
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
}

// fork branches the conversation: the fork continues here, the original
// stays untouched on disk.
func (s *server) fork(arg string) error {
	if s.opts.Session == nil {
		return fmt.Errorf("no session to fork")
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		return fmt.Errorf("wait for the current turn to finish before forking")
	}
	stay, title := session.StayArg(arg)
	ns, err := session.Fork(filepath.Dir(s.opts.Session.Path()), s.opts.Session, title)
	if err != nil {
		return err
	}
	if stay {
		s.notice(fmt.Sprintf("forked → %s · this session stays here, resume the copy to take the other path", ns.Meta.ID))
		return nil
	}
	old := s.opts.Session.Label()
	s.opts.Session = ns
	if err := s.opts.Agent.ForkSession(ns); err != nil {
		s.errf("fork: %v", err)
	}
	s.notice(fmt.Sprintf("forked %s → %s · this session continues from the fork, the original is unchanged", old, ns.Meta.ID))
	s.emit(envelope{Type: "hello", Hello: ptr(s.hello())})
	return nil
}

func (s *server) configKV(arg string) {
	if strings.TrimSpace(arg) == "" {
		s.notice("usage: /config key=value")
		return
	}
	key, val, ok := strings.Cut(arg, "=")
	if !ok {
		s.errf("usage: /config key=value")
		return
	}
	applied, err := agent.ApplyConfig(s.opts.Config, s.opts.Agent, s.opts.ConfigPath, key, val)
	if err != nil {
		s.errf("config: %v", err)
		return
	}
	s.notice("config " + strings.TrimSpace(key) + "=" + fmt.Sprint(applied))
}

func (s *server) pin(arg string) {
	ag := s.opts.Agent
	if arg == "" {
		if len(ag.Pins) == 0 {
			s.notice("no pins: /pin <note> or /pin @path")
			return
		}
		var sb strings.Builder
		for i, p := range ag.Pins {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, textfmt.Clip(strings.ReplaceAll(p, "\n", " "), 120))
		}
		s.sheet("pins", sb.String())
		return
	}
	text, err := agent.PinText(s.opts.Cwd, arg)
	if err != nil {
		s.errf("pin: %v", err)
		return
	}
	arg = text
	ag.Pin(arg)
	s.notice(fmt.Sprintf("pinned (%d)", len(ag.Pins)))
}

func (s *server) export(path string) {
	if path == "" && s.opts.Session != nil {
		path = "janetcode-" + s.opts.Session.Meta.ID + ".md"
	}
	if path == "" {
		s.errf("usage: /export [file.md]")
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.opts.Cwd, path)
	}
	var sb strings.Builder
	for _, e := range s.snapshotEntries() {
		switch e.Kind {
		case "user":
			fmt.Fprintf(&sb, "## user\n\n%s\n\n", e.Text)
		case "assistant":
			fmt.Fprintf(&sb, "## assistant\n\n%s\n\n", e.Text)
		case "tool":
			fmt.Fprintf(&sb, "### %s %s\n\n```\n%s\n```\n\n", e.Name, e.Summary, e.Output)
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		s.errf("export: %v", err)
		return
	}
	s.notice("wrote " + path)
}

func (s *server) snapshotEntries() []*entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*entry(nil), s.entries...)
}
