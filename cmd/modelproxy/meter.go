package main

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"
)

// meterUsage is the usage block of an Anthropic Messages API response, with
// the field names the provider sends so the same struct parses and writes
// them. A field the provider omits stays zero.
type meterUsage struct {
	InputTokens         int `json:"input_tokens,omitempty"`
	OutputTokens        int `json:"output_tokens,omitempty"`
	CacheReadTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// meterRecord is one line of the meter log: one proxied request.
type meterRecord struct {
	ID        int64      `json:"id"`
	Time      time.Time  `json:"time"`
	Path      string     `json:"path"`
	Model     string     `json:"model"`
	Swapped   string     `json:"swapped"`
	Status    int        `json:"status"`
	ElapsedMS int64      `json:"elapsed_ms"`
	Usage     meterUsage `json:"usage"`
}

// meter appends records to the log. Writes are serialised because requests
// run concurrently, and a failed write is logged, never returned: metering
// must not be able to fail a request.
type meter struct {
	mu sync.Mutex
	f  *os.File
	id int64
}

func openMeter(path string) (*meter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &meter{f: f}, nil
}

func (m *meter) close() { _ = m.f.Close() }

func (m *meter) record(r meterRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.id++
	r.ID = m.id
	data, err := json.Marshal(r)
	if err != nil {
		return
	}
	if _, err := m.f.Write(append(data, '\n')); err != nil {
		log.Printf("modelproxy: meter write: %v", err)
	}
}

// parseBodyUsage reads the usage block of a non-streaming JSON response.
func parseBodyUsage(body []byte) meterUsage {
	var v struct {
		Usage meterUsage `json:"usage"`
	}
	if json.Unmarshal(body, &v) != nil {
		return meterUsage{}
	}
	return v.Usage
}

// sseUsage picks the usage out of an Anthropic Messages API event stream as
// the bytes pass through the proxy: message_start carries the initial usage
// (input and cache tokens), message_delta the cumulative output count, so
// the last delta seen is the total. The body is never held whole — feed()
// keeps only the tail of an unfinished line, since a read can split a data
// line anywhere. Bytes it cannot parse are ignored; whatever was seen up to
// that point still stands.
type sseUsage struct {
	pending []byte
	usage   meterUsage
}

func (s *sseUsage) feed(b []byte) {
	s.pending = append(s.pending, b...)
	for {
		i := bytes.IndexByte(s.pending, '\n')
		if i < 0 {
			return
		}
		s.line(s.pending[:i])
		s.pending = s.pending[i+1:]
	}
}

// finish flushes a last line the stream ended without a newline.
func (s *sseUsage) finish() {
	if len(s.pending) > 0 {
		s.line(s.pending)
		s.pending = nil
	}
}

func (s *sseUsage) line(l []byte) {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(l), []byte("data:"))
	if !ok {
		return
	}
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Usage meterUsage `json:"usage"`
		} `json:"message"`
		Usage meterUsage `json:"usage"`
	}
	if json.Unmarshal(bytes.TrimSpace(rest), &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		s.usage.InputTokens = ev.Message.Usage.InputTokens
		s.usage.CacheReadTokens = ev.Message.Usage.CacheReadTokens
		s.usage.CacheCreationTokens = ev.Message.Usage.CacheCreationTokens
		if ev.Message.Usage.OutputTokens > s.usage.OutputTokens {
			s.usage.OutputTokens = ev.Message.Usage.OutputTokens
		}
	case "message_delta":
		// Cumulative, so keep the largest (last) value seen.
		if ev.Usage.OutputTokens > s.usage.OutputTokens {
			s.usage.OutputTokens = ev.Usage.OutputTokens
		}
	}
}
