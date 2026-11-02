// Package effbench ports hero_agent's cross-agent efficiency benchmark: every
// agent under test talks to one metering proxy that records what each request
// cost and which model really answered, a runner launches agents on janetcode's
// own eval tasks, and a report folds the runs into a markdown table.
package effbench

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/provenance"
)

// Record is one proxied request, one JSONL line of meter.jsonl.
type Record struct {
	ID          int       `json:"id"`
	Time        time.Time `json:"time"`
	Path        string    `json:"path"`
	API         string    `json:"api"` // openai | anthropic
	Stream      bool      `json:"stream"`
	Status      int       `json:"status"`
	Asked       string    `json:"asked_model"`
	Served      string    `json:"served_model"`
	Input       int       `json:"input_tokens"`  // all input, cached included
	Cached      int       `json:"cached_tokens"` // served from the prompt cache
	Fresh       int       `json:"fresh_tokens"`  // Input - Cached
	CacheWrite  int       `json:"cache_write_tokens,omitempty"`
	Output      int       `json:"output_tokens"`
	ElapsedMS   int64     `json:"elapsed_ms"`
	TTFBMS      int64     `json:"ttfb_ms"`
	DecodeMS    int64     `json:"decode_ms"` // elapsed - ttfb
	DecodeTPS   float64   `json:"decode_tps"`
	Mismatch    bool      `json:"mismatch,omitempty"`
	BodyChanged string    `json:"body_changed,omitempty"`
}

// Mismatched reports whether the served model belongs to a different vendor
// than the asked one. Both vendors must be known: a renamed model is fine, an
// unknown name is no evidence.
func Mismatched(asked, served string) bool {
	a, s := provenance.Vendor(asked), provenance.Vendor(served)
	return a != "" && s != "" && a != s
}

// usageScan collects usage and the served model from a response, streamed or
// not, for both wire formats. Unparseable bytes are ignored.
type usageScan struct {
	pending []byte
	served  string
	in, out int
	cached  int
	write   int
	seen    bool
}

func (u *usageScan) feed(b []byte) {
	u.pending = append(u.pending, b...)
	for {
		i := bytes.IndexByte(u.pending, '\n')
		if i < 0 {
			return
		}
		u.line(u.pending[:i])
		u.pending = u.pending[i+1:]
	}
}

func (u *usageScan) finish() {
	if len(u.pending) > 0 {
		u.line(u.pending)
		u.pending = nil
	}
}

func (u *usageScan) line(l []byte) {
	rest, ok := bytes.CutPrefix(bytes.TrimSpace(l), []byte("data:"))
	if !ok {
		return
	}
	u.object(bytes.TrimSpace(rest))
}

// wire is the union of the OpenAI and Anthropic response shapes we read.
type wire struct {
	Type    string `json:"type"`
	Model   string `json:"model"`
	Message *struct {
		Model string `json:"model"`
		Usage *wireUsage
	} `json:"message"`
	Usage *wireUsage `json:"usage"`
}

type wireUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	PromptDetails    *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

func (u *usageScan) object(raw []byte) {
	var w wire
	if json.Unmarshal(raw, &w) != nil {
		return
	}
	if w.Model != "" {
		u.served = w.Model
	}
	if w.Message != nil {
		if w.Message.Model != "" {
			u.served = w.Message.Model
		}
		u.usage(w.Message.Usage)
	}
	u.usage(w.Usage)
}

func (u *usageScan) usage(x *wireUsage) {
	if x == nil {
		return
	}
	if x.PromptTokens > 0 || x.CompletionTokens > 0 { // OpenAI
		u.seen = true
		u.in = max(u.in, x.PromptTokens)
		u.out = max(u.out, x.CompletionTokens)
		if x.PromptDetails != nil {
			u.cached = max(u.cached, x.PromptDetails.Cached)
		}
	}
	if x.InputTokens > 0 || x.OutputTokens > 0 || x.CacheRead > 0 || x.CacheCreation > 0 { // Anthropic: input excludes cache
		u.seen = true
		if t := x.InputTokens + x.CacheRead + x.CacheCreation; t > 0 {
			u.in = max(u.in, t)
		}
		u.cached = max(u.cached, x.CacheRead)
		u.write = max(u.write, x.CacheCreation)
		u.out = max(u.out, x.OutputTokens)
	}
}

// Log serialises records to a writer as JSONL.
type Log struct {
	mu   sync.Mutex
	recs []Record
	w    interface{ Write([]byte) (int, error) }
}

func NewLog(w interface{ Write([]byte) (int, error) }) *Log { return &Log{w: w} }

func (l *Log) Add(r Record) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r.ID = len(l.recs) + 1
	r.Fresh = max(r.Input-r.Cached, 0)
	r.DecodeMS = max(r.ElapsedMS-r.TTFBMS, 0)
	if r.DecodeMS > 0 && r.Output > 0 {
		r.DecodeTPS = float64(r.Output) / (float64(r.DecodeMS) / 1000)
	}
	r.Mismatch = r.Served != "" && Mismatched(r.Asked, r.Served)
	l.recs = append(l.recs, r)
	if l.w != nil {
		if b, err := json.Marshal(r); err == nil {
			_, _ = l.w.Write(append(b, '\n'))
		}
	}
}

func (l *Log) Records() []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Record(nil), l.recs...)
}

// apiOf classifies a request path.
func apiOf(path string) string {
	if strings.HasSuffix(strings.SplitN(path, "?", 2)[0], "/messages") {
		return "anthropic"
	}
	return "openai"
}
