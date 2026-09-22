package settingsapply

import (
	"os"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/memory"
)

// BuildDecisions composes the Jev client from the config's "decisions"
// block, or returns nil when there is no block, the master switch is
// off, Off names "decisions", or the key environment variable is empty
// (keyless is true only in that last case, so a caller can say so once).
//
// It is the single composition point: the session build, an eval run
// under --eval-gate and /jev flipping a switch mid-session all wire the
// identical client, including the scrubber. The key is read from the
// environment here and goes no further than the client.
func BuildDecisions(cfg *config.Config, off map[string]bool) (client *jev.Client, keyless bool) {
	if cfg == nil {
		return nil, false
	}
	d := cfg.Decisions
	if !d.On() || off["decisions"] {
		return nil, false
	}
	key := os.Getenv(d.KeyEnv())
	if key == "" {
		return nil, true
	}
	return &jev.Client{
		BaseURL: d.BaseURL,
		Key:     key,
		Model:   d.Model,
		Timeout: time.Duration(d.TimeoutMs) * time.Millisecond,
		// The one scrubbing hook, at the wire so it covers every field
		// of every consumer's state: each credential-shaped span goes
		// out as "[redacted]" and the evidence around it survives.
		Scrub: func(s string) string {
			out, _ := memory.RedactSecrets(s)
			return out
		},
	}, false
}

// WireDecisions puts the decisions block onto an agent: the client (nil
// when the feature is off, which is what every consumer already treats as
// "ask nothing") and each consumer's own flag. It is how the session is
// built and how /jev changes it afterwards, so a toggle mid-session ends
// with exactly the agent a restart would have produced.
func WireDecisions(cfg *config.Config, a *agent.Agent, off map[string]bool) (keyless bool) {
	cl, keyless := BuildDecisions(cfg, off)
	if cl == nil {
		// No client is the feature off, whatever the consumer keys say:
		// a consumer left on with nothing to ask is a status line that
		// lies. A typed nil in an interface is not nil, which is the
		// other reason the nil case is written out rather than shared.
		a.SetDecisions(nil, false, false, false, false)
		return keyless
	}
	d := cfg.Decisions
	a.SetDecisions(cl,
		d.ConsumerOn("triage"), d.ConsumerOn("intake"),
		d.ConsumerOn("route"), d.ConsumerOn("outcome"))
	return keyless
}
