package eval

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/xmonader/janetcode/internal/headless"
)

// A benchmark that drives several harnesses has to read each one's idea of
// "how it went" out of its stdout, and every harness has a different one.
// janetcode's is headless.CLIResult, owned by the package that writes it.

// parseJanetResult recognises janetcode's own result object in a CLI's
// stdout, whether it is the whole of it or the last line of a stream.
// Anything that merely parses as JSON reports false.
func parseJanetResult(stdout []byte) (headless.CLIResult, bool) {
	best, ok := headless.CLIResult{}, false
	try := func(line []byte) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			return
		}
		var v headless.CLIResult
		if json.Unmarshal(line, &v) != nil {
			return
		}
		if v.Type != headless.CLIResultType || v.Harness != "janetcode" {
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
