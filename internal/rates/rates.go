// Package rates prices models from LiteLLM's public rate table, the same
// table ccusage prices against, so cost estimates work for any model the
// user connects without a "prices" entry in the config file. An explicit
// entry still wins; this is the fallback.
package rates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// URL is the raw rate table LiteLLM publishes.
const URL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// TTL is how long a cached table is used before a refresh is attempted.
const TTL = 24 * time.Hour

// maxBytes bounds the download; the table is about a megabyte.
const maxBytes = 16 << 20

// Entry is one model's rates, per token, as LiteLLM records them.
type Entry struct {
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheRead  float64 `json:"cache_read_input_token_cost"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
}

// Table is the parsed rate table.
type Table struct {
	Models    map[string]Entry
	FetchedAt time.Time
}

// CachePath is where the table is kept under the config directory.
func CachePath(cfgDir string) string { return filepath.Join(cfgDir, "model-rates.json") }

// Parse reads the table's JSON. Entries that are not model records (the
// file carries a "sample_spec" key) are skipped, as are models without
// an input or output rate.
func Parse(data []byte) (*Table, error) {
	// Decoding straight into Entry rather than into json.RawMessage and
	// then decoding each value again halves the work: 43.7ms → 19.4ms on
	// the 2.3 MB table, 33,834 → 10,107 allocations. It is all-or-
	// nothing, though — one entry that types a rate as a string fails the
	// whole file — so a failure falls back to the per-entry decode, which
	// drops only the entry it cannot read.
	var direct map[string]Entry
	if err := json.Unmarshal(data, &direct); err == nil {
		t := &Table{Models: make(map[string]Entry, len(direct))}
		for name, e := range direct {
			if name == "sample_spec" || (e.Input == 0 && e.Output == 0) {
				continue
			}
			t.Models[strings.ToLower(name)] = e
		}
		if len(t.Models) == 0 {
			return nil, errors.New("no models in the rate table")
		}
		return t, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	t := &Table{Models: make(map[string]Entry, len(raw))}
	for name, v := range raw {
		if name == "sample_spec" {
			continue
		}
		var e Entry
		if json.Unmarshal(v, &e) != nil || (e.Input == 0 && e.Output == 0) {
			continue
		}
		t.Models[strings.ToLower(name)] = e
	}
	if len(t.Models) == 0 {
		return nil, errors.New("no models in the rate table")
	}
	return t, nil
}

// Load reads the cached table, whatever its age. It returns nil when there
// is none; Fresh says whether a refresh is due.
func Load(cfgDir string) *Table {
	p := CachePath(cfgDir)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	t, err := Parse(data)
	if err != nil {
		return nil
	}
	if st, err := os.Stat(p); err == nil {
		t.FetchedAt = st.ModTime()
	}
	return t
}

// Lazy is a table that has not been read yet. Reading and parsing the
// cached LiteLLM table costs about 20ms of the 2.3 MB file, which is
// most of janetcode's startup and is paid by every run whether or not a
// price is ever asked for — `--inspect`, `--version`, a headless one-
// liner. LoadLazy starts that work in the background instead, so
// startup pays a goroutine and only the first PriceFor waits.
type Lazy struct {
	ready chan struct{}
	table *Table
}

// LoadLazy returns a table that reads itself in the background. The
// returned value is never nil: a missing or corrupt cache yields a table
// that prices nothing, which is what a nil *Table did.
func LoadLazy(cfgDir string) *Lazy {
	l := &Lazy{ready: make(chan struct{})}
	go func() {
		defer close(l.ready)
		l.table = Load(cfgDir)
	}()
	return l
}

// Table blocks until the background read has finished and returns what it
// found, or nil.
func (l *Lazy) Table() *Table {
	if l == nil {
		return nil
	}
	<-l.ready
	return l.table
}

// PriceFor satisfies the interface config.Config holds the table behind.
func (l *Lazy) PriceFor(model string) *llm.Price { return l.Table().PriceFor(model) }

// Fresh reports whether the cached table is younger than TTL.
func Fresh(cfgDir string) bool {
	st, err := os.Stat(CachePath(cfgDir))
	return err == nil && time.Since(st.ModTime()) < TTL
}

// Refresh downloads the table and writes it to the cache. It is meant to
// run in the background at startup; a failure leaves the old cache.
func Refresh(ctx context.Context, cfgDir string) (*Table, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rate table: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	t, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		return nil, err
	}
	tmp := CachePath(cfgDir) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, CachePath(cfgDir)); err != nil {
		return nil, err
	}
	t.FetchedAt = time.Now()
	return t, nil
}

// PriceFor finds a model's rates, per million tokens. Providers name the
// same model differently — "qwen/qwen3.7-plus" through OpenRouter,
// "qwen3.7-plus" at the vendor — so the lookup tries the name as given,
// under LiteLLM's provider prefixes, and stripped of its own prefix.
func (t *Table) PriceFor(model string) *llm.Price {
	if t == nil || model == "" {
		return nil
	}
	m := strings.ToLower(strings.TrimSpace(model))
	candidates := []string{m, "openrouter/" + m}
	if i := strings.LastIndex(m, "/"); i >= 0 {
		base := m[i+1:]
		candidates = append(candidates, base, "openrouter/"+base)
		for _, pre := range []string{"anthropic/", "openai/", "gemini/", "deepseek/", "mistral/", "xai/", "groq/", "dashscope/", "moonshot/", "zhipu/"} {
			candidates = append(candidates, pre+base)
		}
	}
	for _, c := range candidates {
		if e, ok := t.Models[c]; ok {
			return &llm.Price{
				Input:      e.Input * 1e6,
				Output:     e.Output * 1e6,
				CacheRead:  e.CacheRead * 1e6,
				CacheWrite: e.CacheWrite * 1e6,
			}
		}
	}
	return nil
}
