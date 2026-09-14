package agent

import (
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/config"
)

// ApplyConfig writes a catalogued config key onto cfg and the live agent,
// and saves it to path when there is one. /config in every front-end
// comes through here, so a key set in one is set the same way in all.
func ApplyConfig(cfg *config.Config, a *Agent, path, key, val string) (any, error) {
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	applied, err := config.ApplyKV(cfg, key, val)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := config.UpdateKey(path, key, applied); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}
	}
	if a == nil {
		return applied, nil
	}
	switch key {
	case "intent":
		if a.Tools != nil {
			a.Tools.SetIntent(cfg.IntentEnabled())
		}
	case "auto_approve":
		a.UpdateSettings(func(s *Settings) { s.AutoApprove = cfg.AutoApprove })
	case "compact_at":
		a.UpdateSettings(func(s *Settings) { s.CompactAt = cfg.CompactAt })
	case "max_tokens":
		a.UpdateSettings(func(s *Settings) { s.MaxTokens = cfg.MaxTokens })
	case "max_iterations":
		a.UpdateSettings(func(s *Settings) { s.MaxIterations = cfg.MaxIterations })
	case "model":
		a.UpdateSettings(func(s *Settings) { s.Model = cfg.Model })
	case "proof_notes":
		a.UpdateSettings(func(s *Settings) { s.ProofNotes = cfg.ProofNotesEnabled() })
	case "route_cheap":
		a.SetRouteCheap(cfg.RouteCheap)
	}
	return applied, nil
}
