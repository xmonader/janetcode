package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// A headless run is something a script reads. Printing the answer as prose
// on stdout and everything else as prose on stderr is fine for a human and
// useless for a program: what did it cost, how many turns did it take, did
// a permission get denied, did it stop because it ran out of turns or
// because it finished? Those are the questions a benchmark, a CI job or a
// wrapper asks, and they were only answerable by parsing the prose.
//
// Format is how a headless run reports itself.
type Format string

const (
	// FormatText is the original: the answer on stdout, activity on stderr.
	FormatText Format = "text"
	// FormatJSON prints one object at the end, on stdout, and nothing else.
	FormatJSON Format = "json"
	// FormatStreamJSON prints one JSON object per line as things happen,
	// ending with the same result object.
	FormatStreamJSON Format = "stream-json"
)

// ParseFormat validates the --output-format value.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.TrimSpace(s)) {
	case "", FormatText:
		return FormatText, nil
	case FormatJSON:
		return FormatJSON, nil
	case FormatStreamJSON:
		return FormatStreamJSON, nil
	}
	return "", fmt.Errorf("unknown --output-format %q: want text, json or stream-json", s)
}

// Exit codes. A caller that only sees the status still learns which of the
// handful of things that can go wrong went wrong, which is the difference
// between a retry and a bug report.
const (
	ExitOK        = 0
	ExitOther     = 1
	ExitAuth      = 41  // no key, or the endpoint rejected it
	ExitBadInput  = 42  // the flags or the prompt do not make sense
	ExitConfig    = 52  // the configuration file is wrong
	ExitTurnLimit = 53  // the turn hit its model-call cap
	ExitBudget    = 55  // the run reached --max-cost
	ExitCancelled = 130 // interrupted, as a shell reports a SIGINT
)

// ErrBudget is returned when a run reaches its --max-cost ceiling. The
// agent loop raises the same error from inside a turn, so there is one
// error for the one condition.
var ErrBudget = agent.ErrBudget

// ErrBadInput marks a usage error, so main can exit 42 for it.
var ErrBadInput = errors.New("bad input")

// BadInput is a usage error with its own wording: it exits 42 like
// ErrBadInput but prints only the message, not a "bad input:" prefix.
func BadInput(format string, args ...any) error {
	return badInput{fmt.Sprintf(format, args...)}
}

type badInput struct{ msg string }

func (e badInput) Error() string { return e.msg }
func (e badInput) Unwrap() error { return ErrBadInput }

// ErrConfig marks a configuration file that cannot be used, as against
// credentials that are merely missing. Most of these come typed out of
// the config package as config.ErrBadConfig; ErrConfig stays for the
// callers that know a configuration is at fault without the config
// package having said so.
var ErrConfig = errors.New("configuration")

// httpStatusError is any error carrying the HTTP status a provider
// answered with. Both provider packages' APIError implement it; the
// interface is declared here, where it is consumed, so this package
// does not import either of them.
type httpStatusError interface{ HTTPStatus() int }

// HTTPStatus returns the status a provider refused with, and whether the
// error carried one at all. Only a typed provider error counts: a message
// that merely reads like one may have come from anywhere, including a
// proxy or a tool's own output quoted back.
func HTTPStatus(err error) (int, bool) {
	var se httpStatusError
	if errors.As(err, &se) {
		if st := se.HTTPStatus(); st != 0 {
			return st, true
		}
	}
	return 0, false
}

// KeyRejected reports whether err is a provider refusing the key that was
// sent (401 or 403), as opposed to no key having been configured at all.
// The two want different advice, and only the status tells them apart.
// It is what the command line prints its "check the key" hint on, so it
// takes the typed error only: telling someone their key is wrong because
// a tool's output contained "(HTTP 401)" sends them after the wrong bug.
func KeyRejected(err error) bool {
	if err == nil {
		return false
	}
	st, ok := HTTPStatus(err)
	return ok && (st == 401 || st == 403)
}

// looksLikeAuthHTTP is the message-only fallback the exit status still
// honours, for a provider that reports a refusal as prose. It is
// deliberately not part of KeyRejected: exiting 41 on a false positive
// costs a caller one retry, while the hint would send a person after the
// wrong bug.
func looksLikeAuthHTTP(err error) bool {
	s := err.Error()
	return strings.Contains(s, "(HTTP 401)") || strings.Contains(s, "(HTTP 403)")
}

// exitRules is the one mapping from an error to a process status: the
// first row that matches decides, so the order is part of the meaning and
// this is a slice, not a map. Adding a condition means adding a row here
// rather than a branch somewhere in cmd/. Anything unmatched is
// ExitOther.
var exitRules = []struct {
	name string // what the row is about; the test walks these
	is   func(error) bool
	code int
}{
	// Cancellation first: an interrupted run reports the interruption,
	// whatever it happened to be doing when the signal arrived.
	{"cancelled", func(err error) bool {
		return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
	}, ExitCancelled},
	{"budget", func(err error) bool { return errors.Is(err, ErrBudget) }, ExitBudget},
	{"turn limit", func(err error) bool { return errors.Is(err, agent.ErrMaxIterations) }, ExitTurnLimit},
	{"bad input", func(err error) bool {
		return errors.Is(err, ErrBadInput) || errors.Is(err, config.ErrBadOverride)
	}, ExitBadInput},
	{"auth", func(err error) bool {
		return errors.Is(err, llm.ErrNoAPIKey) || errors.Is(err, config.ErrNoCredentials) ||
			KeyRejected(err) || looksLikeAuthHTTP(err)
	}, ExitAuth},
	{"config", func(err error) bool {
		return errors.Is(err, ErrConfig) || errors.Is(err, config.ErrBadConfig)
	}, ExitConfig},
}

// ExitCode classifies an error into one of the codes above. It is the
// only place that mapping lives: cmd/janetcode exits with what this
// returns and the JSON result envelope repeats it.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	for _, r := range exitRules {
		if r.is(err) {
			return r.code
		}
	}
	return ExitOther
}

// recorder watches the events of a run and builds the result envelope.
// For stream-json it also writes one line per event as it happens.
type recorder struct {
	format Format
	out    io.Writer
	start  time.Time

	mu sync.Mutex
	// started is the ids a tool_start has been written for, so a result
	// never arrives for a call the consumer has not seen begin.
	started  map[string]bool
	enc      *json.Encoder
	usage    llm.Usage
	turns    int
	tools    int
	denials  int
	retries  int
	verdict  string
	turnText strings.Builder
	// textBreak is set when a tool ran after some of the turn's text:
	// the next text is another model call's, and running the two
	// together read "I'll list the files.The files are …".
	textBreak bool
	answer    string
	sessionID string
	model     string
	provider  string
	price     *llm.Price
}

func newRecorder(opts Options, format Format) *recorder {
	r := &recorder{format: format, out: opts.Stdout, start: time.Now(), started: map[string]bool{}}
	if format == FormatStreamJSON {
		r.enc = json.NewEncoder(opts.Stdout)
	}
	if opts.Agent != nil {
		r.model, r.price = opts.Agent.Model, opts.Agent.Price
	}
	if opts.Session != nil {
		r.sessionID, r.provider = opts.Session.Meta.ID, opts.Session.Meta.Provider
	}
	return r
}

// text reports whether the caller should also print the human rendering.
func (r *recorder) text() bool { return r.format == FormatText }

// streamEvent is one line of --output-format stream-json.
type streamEvent struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Name       string          `json:"name,omitempty"`
	ID         string          `json:"id,omitempty"`
	Intent     string          `json:"intent,omitempty"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     string          `json:"output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Usage      *CLIUsage       `json:"usage,omitempty"`
	// Attempts and WaitMS describe a retry event: the call failed this
	// many times and spent this long in backoff before it answered. Text
	// carries the reason.
	Attempts int   `json:"attempts,omitempty"`
	WaitMS   int64 `json:"wait_ms,omitempty"`
}

func (r *recorder) emit(e streamEvent) {
	if r.enc == nil {
		return
	}
	_ = r.enc.Encode(e)
}

// event records one loop event, and streams it when asked to. It
// returns the separator text mode prints before an EvText that starts a
// later model call's answer.
func (r *recorder) event(e agent.Event) (sep string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch e.Kind {
	case agent.EvText:
		if r.textBreak && e.Text != "" {
			r.textBreak = false
			sep = "\n\n"
			r.turnText.WriteString(sep)
		}
		r.turnText.WriteString(e.Text)
		r.emit(streamEvent{Type: "text", Text: e.Text})
	case agent.EvThinking:
		r.emit(streamEvent{Type: "thinking", Text: e.Text})
	case agent.EvToolRun:
		r.tools++
		r.textBreak = r.turnText.Len() > 0
		if e.Call != nil {
			r.started[e.Call.ID] = true
			r.emit(streamEvent{Type: "tool_start", Name: e.Call.Name, ID: e.Call.ID, Intent: e.Call.Intent, Input: json.RawMessage(e.Call.Input)})
		}
	case agent.EvToolResult:
		if e.Denied || (e.IsError && strings.HasPrefix(e.Output, agent.DeniedMessage)) {
			r.denials++
		}
		name, id := "", ""
		if e.Call != nil {
			name, id = e.Call.Name, e.Call.ID
		}
		// A call that never ran -- refused by a rule, hidden by read-only,
		// stopped by the budget, or naming a tool that does not exist --
		// produces a result and no EvToolRun, because nothing was about
		// to execute. A consumer pairing results with starts by id then
		// sees a result for a call it never saw begin. The stream is this
		// package's contract, so the missing start is written here rather
		// than by making the loop claim it was going to run the call.
		if e.Call != nil && !r.started[id] {
			r.started[id] = true
			r.emit(streamEvent{Type: "tool_start", Name: name, ID: id, Intent: e.Call.Intent, Input: json.RawMessage(e.Call.Input)})
		}
		r.emit(streamEvent{Type: "tool_result", Name: name, ID: id, Output: e.Output, IsError: e.IsError, DurationMS: e.Duration.Milliseconds()})
	case agent.EvRetry:
		r.retries++
		if e.Retry != nil {
			r.emit(streamEvent{Type: "retry", Text: e.Retry.Reason, Attempts: e.Retry.Attempts, WaitMS: e.Retry.Wait.Milliseconds()})
		}
	case agent.EvDiscard:
		// The dead attempt's deltas are void — the retried call replaces
		// them. Without this the answer would read "half anfull answer".
		r.turnText.Reset()
		r.textBreak = false
		r.started = map[string]bool{}
		// The stream has already carried those deltas as text lines,
		// which cannot be taken back: a line says they are void, or a
		// consumer joining the deltas reads the half answer and the
		// whole one run together.
		r.emit(streamEvent{Type: "discard", Text: "the model call streaming the text above was cut off; its text deltas are void and the retry resends its whole answer"})
	case agent.EvNotice:
		r.emit(streamEvent{Type: "notice", Text: e.Text})
	case agent.EvCompacted:
		r.emit(streamEvent{Type: "compacted", Text: e.Text})
	case agent.EvVerdict:
		r.verdict = e.Text
		r.emit(streamEvent{Type: "verdict", Text: e.Text, IsError: e.IsError})
	case agent.EvTurnDone:
		r.turns++
		r.usage.Add(e.Usage)
		if t := strings.TrimSpace(r.turnText.String()); t != "" {
			r.answer = t
		}
		r.turnText.Reset()
		r.textBreak = false
		u := usageOf(e.Usage)
		r.emit(streamEvent{Type: "turn_done", StopReason: string(e.StopReason), Usage: &u})
	}
	return sep
}

func usageOf(u llm.Usage) CLIUsage {
	return CLIUsage{
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
}

// spentUSD is what the run has cost so far, and whether that is known.
func (r *recorder) spentUSD() (float64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage.Cost(r.price)
}

// CLIReceipts is the evidence-receipt consumer's envelope footprint:
// how many receipts were generated (and stood for a whole oversized
// output), how many the evidence gate rejected (the preview stood
// instead), and the context bytes they removed.
type CLIReceipts struct {
	Generated int `json:"generated"`
	Rejected  int `json:"rejected"`
	SavedB    int `json:"bytes_saved"`
}

// result builds the envelope. modelCalls and toolCalls come from the
// agent's own counters where it has them, which survive compaction.
func (r *recorder) result(ag *agent.Agent, err error) CLIResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	res := CLIResult{
		Type:              CLIResultType,
		Harness:           "janetcode",
		Result:            r.answer,
		ExitCode:          ExitCode(err),
		SessionID:         r.sessionID,
		Model:             r.model,
		Provider:          r.provider,
		Usage:             usageOf(r.usage),
		NumTurns:          r.turns,
		ToolCalls:         r.tools,
		PermissionDenials: r.denials,
		Verdict:           r.verdict,
		DurationMS:        time.Since(r.start).Milliseconds(),
		Retries:           r.retries,
	}
	if ag != nil {
		// The recorder tallies usage from EvTurnDone, which a turn that
		// ended early -- on the budget, on a cancellation, on an error --
		// never sends: the run would report zero tokens for calls it
		// certainly made. The agent counts every call as it returns, so
		// that is the figure when there is one.
		if u := ag.UsageSnapshot(); u.InputTokens+u.OutputTokens+u.CacheReadTokens+u.CacheWriteTokens > 0 {
			res.Usage = usageOf(u)
			if usd, ok := u.Cost(r.price); ok {
				res.CostUSD = usd
			}
			res.CostSource = u.CostSource(r.price)
		}
		st := ag.Stats()
		if st.Retries > 0 {
			// The agent's counter survives a turn that ended early, which
			// the event tally does not.
			res.Retries = st.Retries
		}
		if st.ModelCalls > 0 {
			res.NumTurns = st.ModelCalls
		}
		if calls, _ := st.ToolCalls(); calls > 0 {
			res.ToolCalls = calls
		}
		res.Recoveries = CLIRecoveries{
			ProseToolCalls: st.Recoveries,
			WatchdogKills:  st.WatchdogKills,
			ModelFallbacks: st.Fallbacks,
			Total:          st.Saves(),
		}
		if st.DecisionCalls > 0 {
			open := false
			if ag.Decisions != nil {
				open = !ag.Decisions.Available()
			}
			res.Decisions = &CLIDecisions{
				Calls:       st.DecisionCalls,
				Errors:      st.DecisionErrors,
				CircuitOpen: open,
				Total:       st.DecisionTime.Milliseconds(),
				CostUSD:     st.DecisionCost,
				TokensOut:   st.DecisionTokensOut,
			}
		}
		// The routing evidence rides the session log; a headless reader
		// gets the same per-call record the trajectory shows, so a
		// benchmark can audit routes without replaying the UI.
		if ag.Log != nil {
			for _, e := range ag.Log.Events() {
				if e.Type != session.TypeAssistant || e.Route == "" {
					continue
				}
				res.Routes = append(res.Routes, CLIRoute{
					Turn: e.Turn, Step: e.Step, Slot: e.Slot, Route: e.Route,
					Provider: e.Provider, Model: e.Model, Why: e.RouteWhy,
				})
			}
		}
	}
	if res.CostSource == "" {
		if usd, ok := r.usage.Cost(r.price); ok {
			res.CostUSD = usd
		}
		res.CostSource = r.usage.CostSource(r.price)
	}
	if err != nil {
		res.IsError, res.Error = true, err.Error()
	}
	return res
}

// finish writes the result object for json and stream-json. Text mode has
// already said everything it is going to say.
func (r *recorder) finish(ag *agent.Agent, err error) {
	if r.format == FormatText {
		return
	}
	res := r.result(ag, err)
	resultWritten.Store(true)
	enc := json.NewEncoder(r.out)
	enc.SetIndent("", "  ")
	if r.format == FormatStreamJSON {
		// One object per line, so the last line is the result.
		enc.SetIndent("", "")
	}
	_ = enc.Encode(res)
}

// resultWritten records that a run printed its result object, so the
// process's last word does not print a second one.
var resultWritten atomic.Bool

// FinishEarly prints the result object for a json or stream-json run that
// failed before it got as far as a recorder: a bad flag, a missing key, a
// config that does not load. A script reading stdout used to get nothing
// at all and a parse error of its own; now it gets the same object a
// failed run ends with. It does nothing for text, or when the run already
// printed its result.
func FinishEarly(w io.Writer, format Format, err error) {
	if err == nil || format == FormatText || resultWritten.Load() {
		return
	}
	res := CLIResult{Type: CLIResultType, Harness: "janetcode", IsError: true, Error: err.Error(), ExitCode: ExitCode(err), CostSource: "unpriced"}
	enc := json.NewEncoder(w)
	if format == FormatJSON {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(res)
}
