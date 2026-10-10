package egress

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

// Canaries are honeytokens: environment variables planted in every
// command the model runs, whose values exist nowhere else. Nothing
// legitimate ever reads them, so a value that shows up in a tool call's
// arguments or in a request through the egress proxy is a command
// sending the environment somewhere — the signature of an injected
// "env | curl" — and the run is stopped on the spot. A random value
// cannot match by accident, which is what makes stopping safe.
//
// The names look like credentials a deployment would hold and are
// chosen to collide with nothing a project reads: a project test that
// skipped when STRIPE_SECRET_KEY was unset would run, and fail, on a
// planted one. They also avoid the shapes shell.ScrubEnv removes.
var canaryNames = []string{"DEPLOY_TOKEN_BACKUP", "PROD_DB_PASSWORD_RO"}

// Canary is one planted variable.
type Canary struct {
	Name, Value string
}

// Env is the variable as NAME=value.
func (c Canary) Env() string { return c.Name + "=" + c.Value }

// NewCanaries makes the session's honeytokens from r (crypto/rand when
// nil).
func NewCanaries(r io.Reader) ([]Canary, error) {
	if r == nil {
		r = rand.Reader
	}
	out := make([]Canary, 0, len(canaryNames))
	for i, name := range canaryNames {
		b := make([]byte, 18)
		if _, err := io.ReadFull(r, b); err != nil {
			return nil, err
		}
		// Shaped like what each would hold, so an exfiltration that
		// filters on shape still takes it.
		prefix := []string{"dtk_", "pg_"}[i%2]
		out = append(out, Canary{Name: name, Value: prefix + hex.EncodeToString(b)})
	}
	return out, nil
}

// MatchCanary is the canary whose value appears in s, if any.
func MatchCanary(cs []Canary, s string) (Canary, bool) {
	for _, c := range cs {
		if c.Value != "" && strings.Contains(s, c.Value) {
			return c, true
		}
	}
	return Canary{}, false
}

// ErrCanary is the error a run stops with when a honeytoken was about to
// leave the machine.
var ErrCanary = errors.New("a planted honeytoken was about to leave the machine: the run is stopped, since only a command sending the environment somewhere can hold it (prompt injection is the usual cause; check what the turn read)")
