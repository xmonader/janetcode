// Package settingsapply carries configuration onto a live agent: /config,
// /model and /connect in every front-end come through here, so the agent
// itself does not need to know the config file's shape.
package settingsapply

import (
	"fmt"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/i18n"
	"github.com/xmonader/janetcode/internal/llm"
)

// Apply writes a catalogued config key onto cfg and the live agent,
// and saves it to path when there is one. /config in every front-end
// comes through here, so a key set in one is set the same way in all.
func Apply(cfg *config.Config, a *agent.Agent, path, key, val string) (any, error) {
	key, val = strings.TrimSpace(key), strings.TrimSpace(val)
	applied, err := config.ApplyKV(cfg, key, val)
	if err != nil {
		return nil, err
	}
	if path != "" {
		// A key inside the decisions block writes the block: the file
		// holds one object there, and ApplyKV has already merged the
		// change into the loaded copy of it.
		fileKey, value := key, applied
		if strings.HasPrefix(key, "decisions.") {
			fileKey, value = "decisions", cfg.Decisions
		}
		if strings.HasPrefix(key, "voice.") {
			fileKey, value = "voice", cfg.Voice
		}
		if err := config.UpdateKey(path, fileKey, value); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}
	}
	if a == nil {
		return applied, nil
	}
	// The decisions block is wired as a whole, because its keys are not
	// independent: a consumer switched on with the master switch off has
	// to end up with no client, exactly as a restart would leave it.
	if strings.HasPrefix(key, "decisions.") {
		WireDecisions(cfg, a, a.Off)
		return applied, nil
	}
	switch key {
	case "intent":
		if a.Tools != nil {
			a.Tools.SetIntent(cfg.IntentEnabled())
		}
	case "auto_approve":
		a.UpdateSettings(func(s *agent.Settings) { s.AutoApprove = cfg.AutoApprove })
	case "compact_at":
		a.UpdateSettings(func(s *agent.Settings) { s.CompactAt = cfg.CompactAt })
	case "effort":
		a.UpdateSettings(func(s *agent.Settings) { s.Effort = cfg.Effort })
	case "max_tokens":
		a.UpdateSettings(func(s *agent.Settings) { s.MaxTokens = cfg.MaxTokens })
	case "max_iterations":
		a.UpdateSettings(func(s *agent.Settings) { s.MaxIterations = cfg.MaxIterations })
	case "model":
		// The window and the price belong to the model: set alone, the
		// session compacted against the old model's window and priced
		// its calls at the old model's rates.
		SwitchModel(cfg, a, cfg.Model, nil)
	case "proof_notes":
		a.UpdateSettings(func(s *agent.Settings) { s.ProofNotes = cfg.ProofNotesEnabled() })
	case "route_cheap":
		a.SetRouteCheap(cfg.RouteCheap)
	case "advisor_model":
		a.SetAdvisorModel(cfg.AdvisorModel)
	case "strong_model":
		// Changing the slot invalidates the cached resolution, exactly as
		// /config route_cheap does for the cheap one.
		a.SetStrongModel(cfg.StrongModel)
	case "vision_model":
		a.SetVisionModel(cfg.VisionModel)
	case "language":
		a.SetLanguage(cfg.Language)
		i18n.SetLanguage(i18n.Resolve(cfg.Language, os.Getenv))
	case "ask_timeout", "ask_timeout_unattended":
		ApplyAskWait(cfg, a)
	case "stale_check_nudge":
		a.SetStaleCheckNudge(cfg.StaleCheckNudge)
	case "escalate_after":
		a.SetEscalateAfter(cfg.EscalateAfter)
	case "advisor_mode", "advisor_max_consults":
		a.SetAdvisorMode(cfg.AdvisorMode, cfg.AdvisorMaxConsults)
	case "advisor_turn_start_style":
		a.SetTurnStartStyle(cfg.AdvisorTurnStartStyle)
	case "verify_baseline":
		a.SetVerifyBaseline(cfg.VerifyBaselineEnabled())
	case "sandbox":
		// The key is the project's default profile, so setting it
		// changes what a running sandbox enforces but does not turn one
		// on: that is /sandbox, which can report a backend failure to
		// the person who asked for it.
		switch {
		case a.Sandbox == nil:
		case cfg.Sandbox == "" || cfg.Sandbox == "off":
			a.Sandbox.Disable()
		default:
			if err := a.Sandbox.SetProfile(cfg.Sandbox); err != nil {
				return nil, err
			}
		}
	}
	return applied, nil
}

// SwitchModel moves the live session to model, safe while a turn runs:
// onto prov when it is not nil (the model routes to another provider),
// with the price and context window cfg records for the model — or, with
// no config, the window llm knows for it and the price unchanged. /model
// and /connect in every front-end switch through it, so the four change
// together.
func SwitchModel(cfg *config.Config, a *agent.Agent, model string, prov llm.Provider) {
	if cfg == nil {
		a.UpdateSettings(func(s *agent.Settings) {
			s.Model = model
			if prov != nil {
				s.Provider = prov
			}
			s.ContextWindow = llm.KnownWindow(model)
		})
		return
	}
	a.SwitchModel(model, prov, cfg.PriceFor(model), cfg.MetaFor(model).ContextWindow)
}

// Allowed is whether the managed policy lets the session move to model on
// the provider named provider (of type typ; "" when the provider is not
// changing): /model and /connect in every front-end ask before switching.
// A nil config, or no policy, allows everything.
func Allowed(cfg *config.Config, provider, typ, model string) error {
	if cfg == nil || cfg.Managed == nil {
		return nil
	}
	if provider != "" && !cfg.Managed.ProviderAllowed(provider, typ) {
		return cfg.Managed.Refusal(fmt.Sprintf("provider %q", provider))
	}
	if model != "" && !cfg.Managed.ModelAllowed(model) {
		return cfg.Managed.Refusal(fmt.Sprintf("model %q", model))
	}
	return nil
}

// ApplyAskWait gives the agent the config's two question waits. The
// config was validated at load and by the setter, so an error here can
// only be a value that was valid then; it leaves the agent as it was.
func ApplyAskWait(cfg *config.Config, a *agent.Agent) {
	if in, un, err := cfg.AskWaits(); err == nil {
		a.SetAskWait(in, un)
	}
}
