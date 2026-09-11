package eval

import (
	"bytes"
	"encoding/json"
	"strings"
)

// A benchmark that drives several harnesses has to read each one's idea of
// "how it went" out of its stdout, and every harness has a different one.
// This is janetcode's, defined here rather than in the headless package so
// that the thing which reads it — the bench — does not have to import the
// thing which writes it.

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

// parseJanetResult recognises janetcode's own result object in a CLI's
// stdout, whether it is the whole of it or the last line of a stream.
// Anything that merely parses as JSON reports false.
func parseJanetResult(stdout []byte) (CLIResult, bool) {
	best, ok := CLIResult{}, false
	try := func(line []byte) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			return
		}
		var v CLIResult
		if json.Unmarshal(line, &v) != nil {
			return
		}
		if v.Type != CLIResultType || v.Harness != "janetcode" {
			return
		}
		best, ok = v, true
	}
	try(stdout)
	if ok {
		return best, true
	}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		try(line)
	}
	return best, ok
}

// janetUsage reads spend from janetcode's own envelope. Its input count
// already excludes cache reads, which is what the input column means for
// every harness in this benchmark.
func janetUsage(stdout []byte) (UsageMetrics, bool) {
	v, ok := parseJanetResult(stdout)
	if !ok {
		return UsageMetrics{}, false
	}
	u := UsageMetrics{
		Input:      Int(v.Usage.InputTokens),
		Output:     Int(v.Usage.OutputTokens),
		CacheRead:  Int(v.Usage.CacheReadTokens),
		CacheWrite: Int(v.Usage.CacheWriteTokens),
		Turns:      Int(v.NumTurns),
		ToolCalls:  Int(v.ToolCalls),
	}
	if v.CostUSD > 0 {
		u.CostUSD = Float(v.CostUSD)
	}
	return u, true
}

// janetText pulls the answer out of janetcode's envelope.
func janetText(stdout []byte) string {
	v, ok := parseJanetResult(stdout)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v.Result)
}
