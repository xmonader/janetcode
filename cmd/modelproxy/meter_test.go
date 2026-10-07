package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sseStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","usage":{"input_tokens":1200,"output_tokens":1,"cache_read_input_tokens":800,"cache_creation_input_tokens":50}}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","delta":{"text":"hi"}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","usage":{"output_tokens":17}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// TestSSEUsageAcrossChunks: a data line can be split anywhere by a read;
// the scanner must hold the partial tail until the rest arrives rather
// than drop the event.
func TestSSEUsageAcrossChunks(t *testing.T) {
	stream := sseStream
	// Every split point must give the same usage.
	for _, cut := range []int{1, 7, 40, 137, 200, len(stream) - 1} {
		s := &sseUsage{}
		s.feed([]byte(stream[:cut]))
		s.feed([]byte(stream[cut:]))
		s.finish()
		if s.usage.InputTokens != 1200 || s.usage.CacheReadTokens != 800 || s.usage.CacheCreationTokens != 50 || s.usage.OutputTokens != 17 {
			t.Fatalf("cut at %d: usage = %+v", cut, s.usage)
		}
	}
	// Byte-at-a-time is the worst case.
	s := &sseUsage{}
	for i := 0; i < len(stream); i++ {
		s.feed([]byte{stream[i]})
	}
	s.finish()
	if s.usage.InputTokens != 1200 || s.usage.OutputTokens != 17 {
		t.Fatalf("bytewise: usage = %+v", s.usage)
	}
}

func TestParseBodyUsage(t *testing.T) {
	u := parseBodyUsage([]byte(`{"type":"message","usage":{"input_tokens":42,"output_tokens":9,"cache_read_input_tokens":30}}`))
	if u.InputTokens != 42 || u.OutputTokens != 9 || u.CacheReadTokens != 30 {
		t.Fatalf("usage = %+v", u)
	}
	if u := parseBodyUsage([]byte("not json")); u != (meterUsage{}) {
		t.Fatalf("garbage body must give zero usage, got %+v", u)
	}
}

// TestMeterLogShape is the metered end-to-end path: the proxy streams an
// SSE answer through, and the log line names both models, the status, and
// the usage the provider sent.
func TestMeterLogShape(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseStream)
	}))
	defer up.Close()

	logPath := filepath.Join(t.TempDir(), "meter.jsonl")
	m, err := openMeter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{model: "target/model", upstream: up.URL, key: "secret", quiet: true, meter: m}
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if string(out) != sseStream {
		t.Fatalf("the stream must be forwarded verbatim; got %q", out)
	}
	m.close()

	lines := readMeterLines(t, logPath)
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
	rec := lines[0]
	if rec.ID != 1 {
		t.Fatalf("id = %d, want 1", rec.ID)
	}
	if rec.Model != "claude-sonnet-4-5" || rec.Swapped != "target/model" {
		t.Fatalf("models = %q -> %q", rec.Model, rec.Swapped)
	}
	if rec.Path != "/v1/messages" || rec.Status != 200 {
		t.Fatalf("path/status = %q %d", rec.Path, rec.Status)
	}
	if rec.Time.IsZero() {
		t.Fatal("no timestamp")
	}
	if rec.Usage.InputTokens != 1200 || rec.Usage.OutputTokens != 17 || rec.Usage.CacheReadTokens != 800 || rec.Usage.CacheCreationTokens != 50 {
		t.Fatalf("usage = %+v", rec.Usage)
	}
}

// TestMeteredGarbageStreamForwardsIntact: bytes the scanner cannot parse
// still reach the client unchanged, and the request is still recorded.
func TestMeteredGarbageStreamForwardsIntact(t *testing.T) {
	garbage := "data: {not json\n\n\x00\xffbinary\x02noise\ndata: nope"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, garbage)
	}))
	defer up.Close()

	logPath := filepath.Join(t.TempDir(), "meter.jsonl")
	m, err := openMeter(logPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{model: "m", upstream: up.URL, key: "k", quiet: true, meter: m}
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if string(out) != garbage {
		t.Fatalf("forwarded bytes changed: %q", out)
	}
	m.close()

	lines := readMeterLines(t, logPath)
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1", len(lines))
	}
	if lines[0].Usage != (meterUsage{}) {
		t.Fatalf("garbage must meter zero usage, got %+v", lines[0].Usage)
	}
}

func readMeterLines(t *testing.T, path string) []meterRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []meterRecord
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r meterRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("log line does not parse: %v", err)
		}
		out = append(out, r)
	}
	return out
}
