package config

import (
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/jev"
)

// Which endpoints serve the decisions model, and which consumers exist.
// Both are lists rather than special cases in the front-ends: a second
// endpoint (TypeSafe's native host) and a fourth consumer are expected,
// and neither should mean touching the picker or the toggle command.

// DecisionEndpoint is one service that answers decision questions: the
// base URL to post to and the environment variable its key is read from.
type DecisionEndpoint struct {
	Name    string
	Label   string
	BaseURL string
	KeyEnv  string
}

// DecisionEndpoints are the endpoints a front-end offers, in order. Only
// OpenRouter's decisions route is offered today: it is the one whose wire
// contract is verified live. TypeSafe's native host answers the same
// shapes on https://api.typesafe.ai with TYPESAFE_API_KEY and is reached
// by setting decisions.base_url by hand until it is verified here too.
var DecisionEndpoints = []DecisionEndpoint{
	{Name: "openrouter", Label: "OpenRouter decisions route", BaseURL: jev.DefaultBaseURL, KeyEnv: "OPENROUTER_API_KEY"},
}

// FindDecisionEndpoint looks one up by name.
func FindDecisionEndpoint(name string) (DecisionEndpoint, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, e := range DecisionEndpoints {
		if e.Name == name {
			return e, true
		}
	}
	return DecisionEndpoint{}, false
}

// DecisionEndpointNames lists the offered endpoints, for an error that
// has to say what was expected.
func DecisionEndpointNames() []string {
	out := make([]string, 0, len(DecisionEndpoints))
	for _, e := range DecisionEndpoints {
		out = append(out, e.Name)
	}
	return out
}

// EndpointName names the endpoint a block points at: a listed one when
// the base URL matches (empty means the default, which is the first),
// else "custom", which is a configuration a picker must not silently
// overwrite.
func (d *DecisionsConfig) EndpointName() string {
	if d == nil {
		return ""
	}
	u := strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	if u == "" {
		return DecisionEndpoints[0].Name
	}
	for _, e := range DecisionEndpoints {
		if strings.EqualFold(u, e.BaseURL) {
			return e.Name
		}
	}
	return "custom"
}

// decisionConsumers maps a consumer name to its flag in the block. The
// names are what /jev and /config take; the keys under "decisions" in
// the file are the json tags, which differ for route and outcome because
// the file spells them out.
var decisionConsumers = map[string]struct {
	key   string
	field func(*DecisionsConfig) *bool
}{
	"triage":  {"triage", func(d *DecisionsConfig) *bool { return &d.Triage }},
	"intake":  {"intake", func(d *DecisionsConfig) *bool { return &d.Intake }},
	"route":   {"intake_route", func(d *DecisionsConfig) *bool { return &d.IntakeRoute }},
	"outcome": {"session_outcome", func(d *DecisionsConfig) *bool { return &d.SessionOutcome }},
}

// DecisionConsumers names every consumer /jev can toggle, in order.
var DecisionConsumers = func() []string {
	out := make([]string, 0, len(decisionConsumers))
	for n := range decisionConsumers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}()

// DecisionConsumerKey is the config key under "decisions" a consumer is
// stored as, or "" when the name is not a consumer.
func DecisionConsumerKey(name string) string {
	c, ok := decisionConsumers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return ""
	}
	return "decisions." + c.key
}

// ConsumerOn reports whether a named consumer is on. A missing block,
// an unknown name and a switched-off feature are all off.
func (d *DecisionsConfig) ConsumerOn(name string) bool {
	if !d.On() {
		return false
	}
	c, ok := decisionConsumers[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return false
	}
	return *c.field(d)
}

// AnyConsumerOn reports whether the feature would actually do anything:
// the switch is on and at least one consumer is.
func (d *DecisionsConfig) AnyConsumerOn() bool {
	for _, n := range DecisionConsumers {
		if d.ConsumerOn(n) {
			return true
		}
	}
	return false
}
