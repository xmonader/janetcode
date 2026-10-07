package eval

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeMeterLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "meter.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadMeterLogDegrades: the log is read while the proxy may be
// appending to it, so a missing file and lines that do not parse are
// skipped rather than reported.
func TestLoadMeterLogDegrades(t *testing.T) {
	if rs := LoadMeterLog(filepath.Join(t.TempDir(), "nope.jsonl")); rs != nil {
		t.Fatalf("missing log = %v, want nil", rs)
	}
	path := writeMeterLog(t,
		`{"id":1,"time":"2026-10-07T10:00:01Z","path":"/v1/messages","model":"a","swapped":"b","status":200,"elapsed_ms":900,"usage":{"input_tokens":100,"output_tokens":10}}`,
		`{"id":2,"time":"2026-10-07T10:00:05Z","status":200,"usage":{"input_tokens":200,"output_tokens":20}}`,
		`{"id":3,"time":"2026-10-07T10:0`, // truncated mid-write
		"not json at all",
	)
	rs := LoadMeterLog(path)
	if len(rs) != 2 {
		t.Fatalf("records = %d, want 2", len(rs))
	}
	if rs[0].Usage.InputTokens != 100 || rs[1].Usage.InputTokens != 200 {
		t.Fatalf("usage = %+v / %+v", rs[0].Usage, rs[1].Usage)
	}
}

func TestSumMeterWindow(t *testing.T) {
	path := writeMeterLog(t,
		`{"id":1,"time":"2026-10-07T10:00:00Z","usage":{"input_tokens":50,"output_tokens":5}}`,                                // before
		`{"id":2,"time":"2026-10-07T10:00:10Z","usage":{"input_tokens":100,"output_tokens":10}}`,                              // in
		`{"id":3,"time":"2026-10-07T10:00:20Z","usage":{"input_tokens":200,"output_tokens":20,"cache_read_input_tokens":70}}`, // in
		`{"id":4,"time":"2026-10-07T10:00:30Z","usage":{"input_tokens":400,"output_tokens":40}}`,                              // after
	)
	rs := LoadMeterLog(path)
	start, _ := time.Parse(time.RFC3339, "2026-10-07T10:00:10Z")
	end, _ := time.Parse(time.RFC3339, "2026-10-07T10:00:20Z")
	u := SumMeterWindow(rs, start, end)
	if u.InputTokens != 300 || u.OutputTokens != 30 || u.CacheReadTokens != 70 {
		t.Fatalf("sum = %+v", u)
	}
	if u := SumMeterWindow(nil, start, end); u != (MeterUsage{}) {
		t.Fatalf("no records must sum to zero, got %+v", u)
	}
}

// TestMeterDivergence: a quarter of the proxy total is the line; anything
// closer is agreement, and a side that reports nothing is not checkable.
func TestMeterDivergence(t *testing.T) {
	proxy := MeterUsage{InputTokens: 1000}
	if v := MeterDivergence(proxy, Int(900)); v != "" {
		t.Fatalf("10%% off must agree: %q", v)
	}
	if v := MeterDivergence(proxy, Int(750)); v != "" {
		t.Fatalf("exactly 25%% off must agree: %q", v)
	}
	if v := MeterDivergence(proxy, Int(400)); v == "" {
		t.Fatal("60%% off must flag")
	}
	if v := MeterDivergence(proxy, OptInt{}); v != "" {
		t.Fatalf("harness reported nothing: %q", v)
	}
	if v := MeterDivergence(MeterUsage{}, Int(100)); v != "" {
		t.Fatalf("empty window must not flag: %q", v)
	}
}
