package headless

// The result object is defined here, by the package that writes it; the
// eval bench reads it back from a CLI's stdout (internal/eval's
// cliresult.go) by importing this package.

// CLIResultType is the "type" field of the final object, so a stream of
// events can be told apart from the result that ends it.
const CLIResultType = "result"

// CLIUsage is the token accounting of a whole headless run.
type CLIUsage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// CLIResult is the object `janetcode -p --output-format json` prints, and
// the last line of `--output-format stream-json`.
type CLIResult struct {
	Type    string `json:"type"`
	Harness string `json:"harness"`
	// Result is the assistant's text: what a human would read as the answer.
	Result string `json:"result"`
	// IsError says the run did not finish cleanly; Error says why.
	IsError bool   `json:"is_error"`
	Error   string `json:"error,omitempty"`
	// ExitCode is the process's exit status, repeated here so a caller
	// reading only stdout still has it.
	ExitCode  int      `json:"exit_code"`
	SessionID string   `json:"session_id,omitempty"`
	Model     string   `json:"model,omitempty"`
	Provider  string   `json:"provider,omitempty"`
	Usage     CLIUsage `json:"usage"`
	// CostUSD is what the run cost, and CostSource says where that figure
	// came from: "reported" by the provider, "list price" from a rate
	// table, or "unpriced". A list price is an estimate, not money spent.
	CostUSD           float64 `json:"cost_usd"`
	CostSource        string  `json:"cost_source"`
	NumTurns          int     `json:"num_turns"`
	ToolCalls         int     `json:"tool_calls"`
	PermissionDenials int     `json:"permission_denials"`
	// Verdict is what the turn demonstrated, computed from what the
	// harness watched rather than from what the model claimed.
	Verdict    string `json:"verdict,omitempty"`
	DurationMS int64  `json:"duration_ms"`
	// Recoveries is the harness's repair work: what it had to rescue for
	// the run to get this far. A run with a clean result and three
	// recoveries is not the same run as one with none, and a benchmark
	// that cannot see the difference credits the model with the
	// harness's work.
	Recoveries CLIRecoveries `json:"recoveries"`
	// Retries counts model calls that failed transiently and succeeded on
	// a later attempt. The wait is already in duration_ms; this says how
	// much of it was the provider refusing rather than thinking.
	Retries int `json:"retries"`
	// Decisions is the optional decision provider's footprint (internal/
	// jev): null until its first call, so a session that never consulted
	// it is indistinguishable from one without the feature configured.
	Decisions *CLIDecisions `json:"decisions,omitempty"`
}

// CLIDecisions is what the envelope says about the decision provider:
// how many questions it was asked, how many failed, whether its circuit
// ended the session closed, and the latency it cost — which a run
// comparing against the feature costs must see alongside the pass rate.
type CLIDecisions struct {
	Calls       int   `json:"calls"`
	Errors      int   `json:"errors"`
	CircuitOpen bool  `json:"circuit_open"`
	Total       int64 `json:"total_latency_ms"` // milliseconds
	// CostUSD is what the provider itself billed for those calls, and
	// TokensOut the tokens behind it. Both are omitted when the service
	// reported no figure, so a zero here is never a local estimate.
	CostUSD   float64 `json:"cost_usd,omitempty"`
	TokensOut int     `json:"output_tokens,omitempty"`
}

// CLIRecoveries counts the ways a run was saved rather than smooth.
type CLIRecoveries struct {
	// ProseToolCalls were written as text and read back as calls.
	ProseToolCalls int `json:"prose_tool_calls"`
	// WatchdogKills are streams that stopped producing and were ended.
	WatchdogKills int `json:"watchdog_kills"`
	// ModelFallbacks are calls that only succeeded on another model.
	ModelFallbacks int `json:"model_fallbacks"`
	// Total is the sum, for a caller that only wants to know whether
	// anything needed rescuing.
	Total int `json:"total"`
}
