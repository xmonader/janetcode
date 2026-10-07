package eval

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"time"
)

// MeterUsage is the usage block of one modelproxy meter record: the tokens
// the provider reported for one proxied request.
type MeterUsage struct {
	InputTokens         int `json:"input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// MeterRecord is one line of a modelproxy -meter JSONL log.
type MeterRecord struct {
	ID        int64      `json:"id"`
	Time      time.Time  `json:"time"`
	Path      string     `json:"path"`
	Model     string     `json:"model"`
	Swapped   string     `json:"swapped"`
	Status    int        `json:"status"`
	ElapsedMS int64      `json:"elapsed_ms"`
	Usage     MeterUsage `json:"usage"`
}

// LoadMeterLog reads a modelproxy meter log. A missing file is not an
// error, and neither is a line that does not parse: the proxy appends while
// the bench runs, so the last line can be read mid-write.
func LoadMeterLog(path string) []MeterRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []MeterRecord
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r MeterRecord
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// SumMeterWindow totals the usage of the records timestamped inside one
// task's wall-clock window [start, end].
func SumMeterWindow(rs []MeterRecord, start, end time.Time) MeterUsage {
	var u MeterUsage
	for _, r := range rs {
		if r.Time.Before(start) || r.Time.After(end) {
			continue
		}
		u.InputTokens += r.Usage.InputTokens
		u.OutputTokens += r.Usage.OutputTokens
		u.CacheReadTokens += r.Usage.CacheReadTokens
		u.CacheCreationTokens += r.Usage.CacheCreationTokens
	}
	return u
}

// MeterDivergence compares what the proxy metered with the input tokens the
// harness reported for the same window, returning a verdict when they
// diverge by more than a quarter of the proxy's total, and "" when they
// agree — or when there is nothing to check against: the harness did not
// report input tokens, or the window held no metered requests. The proxy
// counted what the provider billed, so a large gap means the harness's own
// telemetry cannot be trusted for this run, not that the run failed.
func MeterDivergence(proxy MeterUsage, reported OptInt) string {
	if !reported.Set || proxy.InputTokens <= 0 {
		return ""
	}
	diff := math.Abs(float64(reported.Value - proxy.InputTokens))
	if diff <= 0.25*float64(proxy.InputTokens) {
		return ""
	}
	return fmt.Sprintf("meter cross-check: harness reported %d input tokens, the proxy metered %d in the task window", reported.Value, proxy.InputTokens)
}
