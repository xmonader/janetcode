package headless

import (
	"encoding/json"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/dossier"
	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/evidence"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/proof"
	"github.com/xmonader/janetcode/internal/scope"
)

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
	Verdict string `json:"verdict,omitempty"`
	// Evidence is the session's verification evidence on the one status
	// model (internal/evidence): every check with its status, exit code,
	// timing and freshness against the latest edit, and the final status
	// that says whether a success claim is backed. It never changes the
	// exit code, which stays authoritative. Omitted when nothing was
	// edited or checked.
	Evidence *evidence.Report `json:"evidence,omitempty"`
	// Conflict is the CONFLICT the last turn ended on (conflict_exit):
	// the cited test, the task's words, why, and whether the harness
	// confirmed the test exists and fails. Only a confirmed one sets
	// exit status 58.
	Conflict   *proof.Conflict `json:"conflict,omitempty"`
	DurationMS int64           `json:"duration_ms"`
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
	// Served is what the last model call's response said about who served
	// it (llm.Provenance); Mismatches counts the calls whose response named
	// another vendor than the requested model's, and Mismatch is the first
	// verdict. A benchmark reading this result can tell the model it asked
	// for from the one a gateway substituted.
	Served     *llm.Provenance `json:"served,omitempty"`
	Mismatches int             `json:"provenance_mismatches,omitempty"`
	Mismatch   string          `json:"provenance_mismatch,omitempty"`
	// Decisions is the optional decision provider's footprint (internal/
	// jev): null until its first call, so a session that never consulted
	// it is indistinguishable from one without the feature configured.
	Decisions *CLIDecisions `json:"decisions,omitempty"`
	// Routes is the routing evidence for every model call that left the
	// session's own model (github issue #4): one entry per detour, in
	// the order the calls were made. Main calls are not listed — no
	// detour, nothing to report — and an empty list (omitted) means the
	// run never left its own model.
	Routes []CLIRoute `json:"routes,omitempty"`
	// StructuredOutput is the answer as data, validated against
	// --json-schema; absent without the flag or when no valid answer came.
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	// Egress is what each turn of the run sent off the machine: the
	// hosts, the bytes, the files and tool output that went into the
	// model requests, memory and skills attached, and how many
	// secret-shaped values were redacted first (see internal/egress).
	Egress []egress.Report `json:"egress,omitempty"`
	// Scope is the run's edits against the scope inferred from its task
	// (internal/scope): the files outside it with their justification,
	// and the creep share. Omitted when the task named nothing.
	Scope *scope.Report `json:"scope,omitempty"`
	// Dossier is a failed run's hand-off (internal/dossier), also written
	// beside the session log as <id>.dossier.md. Omitted on success.
	Dossier *dossier.Dossier `json:"dossier,omitempty"`
	// DryRun is the preview a --dry-run run built instead of changing
	// anything: the files it would change with their diffs, and every
	// call it recorded rather than made. Omitted outside a dry run.
	DryRun *CLIDryRun `json:"dry_run,omitempty"`
	// CacheMisses is what the run paid for twice because the prompt
	// prefix stopped matching, by cause (the cache ledger). Omitted when
	// the run made fewer than two model calls.
	CacheMisses *agent.CacheMisses `json:"cache_misses,omitempty"`
}

// CLIDryRun is a dry run's preview in the result object: the same content
// /dryrun show gives in the terminal.
// plus the mode, sandbox, hosts, verify commands and the model-call and
// cost estimate with its basis (agent.DryPlan).
type CLIDryRun = agent.DryPlan

// CLIRoute is one model call served away from the session's own model.
// Route is the kind of detour: "route" for a cheap step, "escalate" for
// the failure cascade, "fallback" after a provider failure. Why is the
// reason in the policy's own words, exactly as the session log records
// it, so a benchmark can group or filter routes without a second source.
type CLIRoute struct {
	Turn     int    `json:"turn"`
	Step     int    `json:"step"`
	Slot     string `json:"slot"`
	Route    string `json:"route"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Why      string `json:"why,omitempty"`
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
	// RejectedToolCalls are malformed calls the provider refused and the
	// model was asked to make again.
	RejectedToolCalls int `json:"rejected_tool_calls"`
	// WatchdogKills are streams that stopped producing and were ended.
	WatchdogKills int `json:"watchdog_kills"`
	// ModelFallbacks are calls that only succeeded on another model.
	ModelFallbacks int `json:"model_fallbacks"`
	// Total is the sum, for a caller that only wants to know whether
	// anything needed rescuing.
	Total int `json:"total"`
}
