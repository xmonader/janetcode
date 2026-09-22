// Package jev is a minimal client for TypeSafe's System One decision API
// (the Jev model): a state and a set of typed questions in, probability
// distributions and confidences out, with no text generation. janetcode
// uses it for advisory decisions around the agent loop — verification
// triage first — never for anything the harness can decide exactly, and
// never in the permission path (see docs/notes/jev.md and
// docs/notes/jev-integration-spec.md).
//
// The client is deliberately a leaf: it imports nothing from janetcode,
// retries nothing on the live path (a retried decision is a late
// decision), and fails soft — every error is meant to be handled as
// "no opinion" by the caller.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is OpenRouter's decisions endpoint, the route whose
// wire contract is verified live (model ~typesafe/jev-latest; the
// response reports the pinned version, e.g. typesafe/jev-1.13-…).
// TypeSafe's native host answers on /v1/systemone with the same shapes
// and is selected by pointing base_url at it.
const DefaultBaseURL = "https://openrouter.ai"

// DefaultModel is the slug a front-end offers when nobody has named a
// version: the one the wire contract is verified against. It is a slug
// and not an alias — the service resolves it and every answer reports
// the pinned version it actually ran — so PinnedModel accepts it.
const DefaultModel = "~typesafe/jev-latest"

// DefaultTimeout bounds one decision call. Jev's value is cheap
// immediacy; past MaxTimeout the answer is not worth waiting for.
const (
	DefaultTimeout = 800 * time.Millisecond
	MaxTimeout     = 2 * time.Second
)

// Kind is the question type, matching the API's three primitives.
type Kind string

const (
	// Noul asks a yes/no question; the answer is P(true).
	Noul Kind = "noul"
	// Choice picks one of Options (id → description).
	Choice Kind = "choice"
	// Score places the state on an ordered rubric of Levels.
	Score Kind = "score"
)

// Question is one typed question evaluated against the state.
type Question struct {
	ID           string
	Instructions string
	Kind         Kind
	// Options are a Choice's candidates, id to description. The id is
	// what Answer.Choice returns.
	Options map[string]string
	// Levels are a Score's rubric, lowest first; the answer's Score is
	// a position on it.
	Levels []string
	// NoulCriteria spells out what true and false mean for a Noul. It
	// is optional — the wire contract allows exactly the keys "true"
	// and "false", and the question is sent without it when empty.
	NoulCriteria map[string]string
}

// Answer is one question's result.
type Answer struct {
	Kind          Kind
	Noul          float64            // Kind Noul: P(true)
	Choice        string             // Kind Choice: the winning option id
	Score         float64            // Kind Score: the level index
	Confidence    float64            // Choice and Score only
	Probabilities map[string]float64 // Choice and Score only
}

// Result is one Evaluate call: the answers plus what it cost and how
// long it took. Model is the versioned id the server reports answering
// (jev-1.13.0), which may differ from the alias asked for.
type Result struct {
	Model    string
	Answers  map[string]Answer
	TokensIn int
	// TokensOut and Cost are what the call was billed. Cost is the
	// server's own USD figure and is zero when it did not send one —
	// never a local estimate, because a decision model's price is not
	// in the harness's rate table.
	TokensOut  int
	Cost       float64
	Latency    time.Duration
	StatusCode int
}

// Noul is the probability for a Noul question, or false when the
// answer is absent or of another kind.
func (r *Result) NoulAt(id string) (float64, bool) {
	a, ok := r.Answers[id]
	if !ok || a.Kind != Noul {
		return 0, false
	}
	return a.Noul, true
}

// ChoiceAt is the selected option id and its confidence for a Choice
// question, or false when the answer is absent or of another kind. A
// Score answer is deliberately not one: its score is a continuous
// position on the rubric, not an option id — ScoreAt reads those.
func (r *Result) ChoiceAt(id string) (choice string, confidence float64, ok bool) {
	a, ok := r.Answers[id]
	if !ok || a.Kind != Choice {
		return "", 0, false
	}
	return a.Choice, a.Confidence, true
}

// ScoreAt is a Score answer's position on the rubric, the distribution
// over the level indices and its confidence, or false when the answer
// is absent or of another kind. The score is continuous — 1.035 sits
// between levels 1 and 2 — while the keys of probs are the integer
// level indices as strings ("0", "1", "2"), so the score is not one of
// them and must not be stringified into one.
func (r *Result) ScoreAt(id string) (score float64, probs map[string]float64, confidence float64, ok bool) {
	a, ok := r.Answers[id]
	if !ok || a.Kind != Score {
		return 0, nil, 0, false
	}
	return a.Score, a.Probabilities, a.Confidence, true
}

// wire types: the API's request and response shapes.

// The API spells a choice's options and a score's rubric both as
// "criteria": an object for choice, an array for score. Encoding that
// needs a custom marshaler on the question, not two structs nobody can
// tell apart.
func (q Question) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": string(q.Kind), "instructions": q.Instructions}
	switch q.Kind {
	case Choice:
		m["criteria"] = q.Options
	case Score:
		m["criteria"] = q.Levels
	case Noul:
		// Optional here, and omitted when empty so a question without
		// it goes out byte for byte as it always did.
		if len(q.NoulCriteria) > 0 {
			m["criteria"] = q.NoulCriteria
		}
	}
	return json.Marshal(m)
}

type wireRequest struct {
	State     string              `json:"state"`
	Model     string              `json:"model"`
	Questions map[string]Question `json:"questions"`
}

type wireAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Noul          float64            `json:"noul"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int     `json:"input_tokens"`
		OutputTokens int     `json:"output_tokens"`
		Cost         float64 `json:"cost"`
	} `json:"usage"`
}

// StatusError is a non-200 answer from the decisions endpoint. Every
// error is still soft by contract — the live path treats it as "no
// opinion" and never retries — but the offline replay needs to tell a
// rate limit from a bad request, so the status and any Retry-After the
// server sent travel with the message instead of being formatted away.
type StatusError struct {
	StatusCode int
	Message    string
	// RetryAfter is the server's Retry-After header, parsed from
	// either seconds or an HTTP date, and zero when absent or
	// unparseable.
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("decisions %d: %s", e.StatusCode, e.Message)
}

// Retryable reports whether the same request could plausibly succeed
// later: a rate limit or a server-side failure, never a 4xx the
// request itself earned.
func (e *StatusError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// parseRetryAfter reads the header in both forms the spec allows:
// delay-seconds and an HTTP date. Anything else, or a date in the
// past, is no delay at all.
func parseRetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(h, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

type wireError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

// Client is one configured Jev endpoint. It is safe for concurrent use.
type Client struct {
	BaseURL string // default DefaultBaseURL
	Key     string
	// Model is the pinned, versioned model id. Config validation refuses
	// aliases (jev-latest): a threshold tuned against one version must
	// not silently retune itself when the alias moves.
	Model   string
	Timeout time.Duration // clamped to (0, MaxTimeout]; default DefaultTimeout
	HTTP    *http.Client  // default http.DefaultClient

	// Scrub, when set, rewrites the state before it leaves the process
	// (the composition root wires memory.ScanSecret into it). Returning
	// "[redacted]" for a flagged field is the convention.
	Scrub func(string) string

	// NoBreaker turns the circuit breaker off for this client. A live
	// session must never set it — a decision service that is down must
	// not tax every later gate with its timeout. The offline tools set
	// it: in `--decisions-replay` a burst of 503s would otherwise open
	// the circuit and turn every remaining row into an error row, which
	// is a corpus lost to a transient outage rather than a calibration
	// result; in `doctor` the probe is the only call there is.
	NoBreaker bool

	// Cooldown is how long an open circuit stays shut before it lets a
	// single probe call through (DefaultCooldown when zero). The probe
	// is the only concession: a service that is down costs this session
	// one call per cooldown, not one per gate failure.
	Cooldown time.Duration

	// now is the clock, injected by the tests so a cooldown does not
	// have to elapse in real time. time.Now when nil.
	now func() time.Time

	mu         sync.Mutex
	consecErrs int
	latencies  [LatencyWindow]time.Duration
	latN       int
	latIdx     int
	open       bool
	openedAt   time.Time
	probing    bool
	calls      int
	errs       int
	totalTime  time.Duration
}

// DefaultCooldown is how long the breaker holds an open circuit before
// probing. Long enough that a dead service is not polled, short enough
// that a blip does not cost a whole session its triage.
const DefaultCooldown = 5 * time.Minute

// maxConsecErrors is how many failures in a row close the circuit for
// the session. A decision service that is down must not tax every later
// gate with its timeout.
const maxConsecErrors = 3

// LatencyWindow is how many recent successful calls the latency
// breaker looks at, and the ring above its size.
const (
	LatencyWindow = 10
	// The spec's rung 4: a service answering ten calls in a row with a
	// median past 1s is not a service worth the turn's latency budget,
	// however available it is. The median is median: one slow call is
	// noise, ten slow ones are the service.
	LatencyOpenP50 = time.Second
)

// endpoint is the one path per host: OpenRouter serves the Decisions
// API at /api/alpha/decisions, TypeSafe's native host at
// /v1/systemone. The request and response bodies are the same shape on
// both, which the live and fake-server tests pin. An OpenRouter base
// that carries the chat-completions suffix (/api/v1, from copying the
// chat preset) is normalised rather than 404ing on the first call.
func (c *Client) endpoint() string {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/api/v1")
	if strings.Contains(base, "openrouter.ai") {
		return base + "/api/alpha/decisions"
	}
	return base + "/v1/systemone"
}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	if c.Timeout > MaxTimeout {
		return MaxTimeout
	}
	return c.Timeout
}

// Available reports whether a call is worth attempting at all: the
// circuit is closed, or it is open but due a probe and no probe is
// already in flight. It never blocks and it claims nothing — callers
// use it as a cheap gate before building a state, and Evaluate does
// the claiming.
func (c *Client) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.open || (c.probeDueLocked() && !c.probing)
}

func (c *Client) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Client) cooldown() time.Duration {
	if c.Cooldown <= 0 {
		return DefaultCooldown
	}
	return c.Cooldown
}

// probeDueLocked reports whether the cooldown since the circuit opened
// has elapsed. Caller holds mu.
func (c *Client) probeDueLocked() bool {
	return !c.clock().Before(c.openedAt.Add(c.cooldown()))
}

// allow claims the right to make one call. probe is true when this call
// is the half-open probe, which record settles: success closes the
// circuit, failure re-opens it for another cooldown. Exactly one probe
// is in flight at a time, which is why the claim happens under the same
// lock that reads the state.
func (c *Client) allow() (probe, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return false, true
	}
	if c.probing || !c.probeDueLocked() {
		return false, false
	}
	c.probing = true
	return true, true
}

// Stats are the session's counters, for /stats and the JSON envelope:
// calls attempted, how many failed, and the total time spent waiting.
func (c *Client) Stats() (calls, errs int, total time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.errs, c.totalTime
}

// record folds one call's outcome into the circuit and the counters.
// probe says this call was the half-open probe: it settles the claim
// either way, closing the circuit on success and starting a fresh
// cooldown on failure.
func (c *Client) record(d time.Duration, err error, probe bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.totalTime += d
	if probe {
		c.probing = false
		if err != nil {
			c.errs++
			c.openedAt = c.clock()
			return
		}
		// A probe that answered closes the circuit and starts both
		// breakers from scratch: the error streak that opened it is
		// spent, and the latency window it opened on is stale — judging
		// the next call against ten old slow samples would re-open the
		// circuit on the first success.
		c.open = false
		c.consecErrs = 0
		c.latN, c.latIdx = 0, 0
		return
	}
	if err == nil {
		c.consecErrs = 0
		c.latencies[c.latIdx] = d
		c.latIdx = (c.latIdx + 1) % LatencyWindow
		if c.latN < LatencyWindow {
			c.latN++
		}
		// Checked on the call that fills the window too: ten slow calls
		// in a row are the service, whatever order they arrived in.
		if !c.NoBreaker && c.latN == LatencyWindow && p50(&c.latencies, LatencyWindow) > LatencyOpenP50 {
			c.open, c.openedAt = true, c.clock()
		}
		return
	}
	c.errs++
	c.consecErrs++
	if !c.NoBreaker && c.consecErrs >= maxConsecErrors {
		c.open, c.openedAt = true, c.clock()
	}
}

// p50 is the lower median of the first n elements, in place.
func p50(vs *[LatencyWindow]time.Duration, n int) time.Duration {
	s := append([]time.Duration(nil), vs[:n]...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[n/2]
}

// Evaluate answers every question against state in one request. The
// caller's ctx is bounded further by the client's timeout. Any error —
// network, non-200, malformed body — is soft by contract: callers treat
// it as "no opinion" and behave exactly as if Jev were not configured.
func (c *Client) Evaluate(ctx context.Context, state string, qs []Question) (*Result, error) {
	if !c.Available() {
		return nil, fmt.Errorf("decisions circuit is open")
	}
	if c.Key == "" {
		return nil, fmt.Errorf("no API key")
	}
	if len(qs) == 0 {
		return nil, fmt.Errorf("no questions")
	}
	if c.Scrub != nil {
		state = c.Scrub(state)
	}
	questions := make(map[string]Question, len(qs))
	for _, q := range qs {
		questions[q.ID] = q
	}
	body, err := json.Marshal(wireRequest{State: state, Model: c.Model, Questions: questions})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	// Claimed here and not at the top: every return path from this point
	// settles it through record, so a half-open probe can never be lost
	// to a marshalling error and leave the circuit stuck.
	probe, ok := c.allow()
	if !ok {
		return nil, fmt.Errorf("decisions circuit is open")
	}
	resp, err := hc.Do(req)
	d := time.Since(start)
	if err != nil {
		c.record(d, err, probe)
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		c.record(d, err, probe)
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		var we wireError
		msg := http.StatusText(resp.StatusCode)
		if json.Unmarshal(data, &we) == nil {
			if we.Message != "" {
				msg = we.Message
			} else if we.Error.Message != "" {
				msg = we.Error.Message
			}
		}
		err := &StatusError{StatusCode: resp.StatusCode, Message: msg,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())}
		c.record(d, err, probe)
		return nil, err
	}
	var wr wireResponse
	if err := json.Unmarshal(data, &wr); err != nil {
		c.record(d, err, probe)
		return nil, fmt.Errorf("decisions: malformed response: %w", err)
	}
	res := &Result{
		Model:      wr.Model,
		Answers:    make(map[string]Answer, len(wr.Answers)),
		TokensIn:   wr.Usage.InputTokens,
		TokensOut:  wr.Usage.OutputTokens,
		Cost:       wr.Usage.Cost,
		Latency:    d,
		StatusCode: resp.StatusCode,
	}
	for id, wa := range wr.Answers {
		res.Answers[id] = Answer{
			Kind: Kind(wa.Type), Noul: wa.Noul, Choice: wa.Choice,
			Score: wa.Score, Confidence: wa.Confidence, Probabilities: wa.Probabilities,
		}
	}
	c.record(d, nil, probe)
	return res, nil
}

// PinnedModel reports whether model names a model the harness can hold
// a threshold against: a versioned native id (jev-1.13.0) or one of
// OpenRouter's ~typesafe/jev-* decision slugs, whose responses always
// report the pinned version. Bare aliases ("jev-latest", "jev-preview")
// without the vendor prefix are refused: a threshold tuned against one
// version must not silently retune itself when the alias moves.
func PinnedModel(model string) bool {
	if rest, ok := strings.CutPrefix(model, "~typesafe/"); ok && strings.HasPrefix(rest, "jev") && rest != "jev" {
		return true
	}
	rest, ok := strings.CutPrefix(model, "jev-")
	if !ok {
		return false
	}
	dots := 0
	for _, r := range rest {
		switch {
		case r == '.':
			dots++
		case r < '0' || r > '9':
			return false
		}
	}
	return dots == 2
}
