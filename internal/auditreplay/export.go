package auditreplay

import (
	"encoding/json"
	"fmt"

	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/session"
)

// BundleFormat names the layout of an exported audit bundle.
const BundleFormat = "janetcode-audit/1"

// Bundle is the portable audit artifact: the whole session record, the
// verdict of the audit chain and of the offline replay at export time,
// and nothing that would let a reader act as the user.
type Bundle struct {
	Format  string       `json:"format"`
	Session session.Meta `json:"session"`
	Audit   AuditVerdict `json:"audit"`
	Replay  ReplayResult `json:"replay"`
	// Redactions is how many credential-shaped spans were replaced by
	// secret.Placeholder across the exported events. The audit and replay
	// verdicts were computed over the unredacted record, before this
	// replacement, and are recorded here: a redacted event no longer
	// matches its chain digest or request digest, by design.
	Redactions int               `json:"redactions"`
	Events     []json.RawMessage `json:"events"`
}

// AuditVerdict is session.AuditReport as the bundle records it.
type AuditVerdict struct {
	OK       bool     `json:"ok"`
	Records  int      `json:"records"`
	Events   int      `json:"events"`
	Head     string   `json:"head,omitempty"`
	Problems []string `json:"problems,omitempty"`
}

// ReplayResult is Report as the bundle records it.
type ReplayResult struct {
	OK        bool     `json:"ok"`
	Calls     int      `json:"model_calls"`
	Mutations int      `json:"mutations"`
	Checks    int      `json:"checks"`
	Problems  []string `json:"problems,omitempty"`
}

// Export builds the bundle for a session. Every string in every event is
// passed through secret.Redact, which also removes every value registered
// with secret.Register (a caller registers the configured API keys first);
// the system prompt, tool schemas and messages the replay needs are
// events, so they are covered too. The replay verdict is taken from the
// unredacted events.
func Export(meta session.Meta, audit session.AuditReport, events []session.Event) (Bundle, error) {
	rep := Replay(events)
	b := Bundle{
		Format:  BundleFormat,
		Session: meta,
		Audit: AuditVerdict{OK: audit.OK(), Records: audit.Records, Events: audit.Events,
			Head: audit.Head, Problems: audit.Problems},
		Replay: ReplayResult{OK: rep.OK(), Calls: len(rep.Steps), Mutations: rep.Mutations,
			Checks: rep.Checks, Problems: rep.Problems},
	}
	b.Session.Pins = redactStrings(b.Session.Pins, &b.Redactions)
	b.Session.Title = redactString(b.Session.Title, &b.Redactions)
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			return b, fmt.Errorf("event seq %d: %w", e.Seq, err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return b, fmt.Errorf("event seq %d: %w", e.Seq, err)
		}
		red, err := json.Marshal(redactValue(v, &b.Redactions))
		if err != nil {
			return b, fmt.Errorf("event seq %d: %w", e.Seq, err)
		}
		b.Events = append(b.Events, red)
	}
	for i, p := range b.Replay.Problems {
		b.Replay.Problems[i] = redactString(p, &b.Redactions)
	}
	return b, nil
}

// Marshal is the bundle as indented JSON.
func (b Bundle) Marshal() ([]byte, error) { return json.MarshalIndent(b, "", "  ") }

// ReadBundle parses an exported bundle.
func ReadBundle(data []byte) (Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return b, fmt.Errorf("not an audit bundle: %w", err)
	}
	if b.Format != BundleFormat {
		return b, fmt.Errorf("not an audit bundle: format %q, want %q", b.Format, BundleFormat)
	}
	return b, nil
}

// SessionEvents decodes the bundle's events.
func (b Bundle) SessionEvents() ([]session.Event, error) {
	out := make([]session.Event, 0, len(b.Events))
	for i, raw := range b.Events {
		var e session.Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, fmt.Errorf("bundle event %d: %w", i, err)
		}
		out = append(out, e)
	}
	return out, nil
}

func redactString(s string, n *int) string {
	out, c := secret.Redact(s)
	*n += c
	return out
}

func redactStrings(in []string, n *int) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redactString(s, n)
	}
	return out
}

// redactValue redacts every string in a decoded JSON value, keys included.
func redactValue(v any, n *int) any {
	switch t := v.(type) {
	case string:
		return redactString(t, n)
	case []any:
		for i := range t {
			t[i] = redactValue(t[i], n)
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[redactString(k, n)] = redactValue(x, n)
		}
		return out
	}
	return v
}
