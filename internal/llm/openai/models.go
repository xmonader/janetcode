package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// ListModels implements llm.ModelLister via GET /models.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if k, _ := p.ring.Current(); k != "" {
		hreq.Header.Set("Authorization", "Bearer "+k)
	}
	resp, err := p.http.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(raw))
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	var out struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			MaxModelLen   int    `json:"max_model_len"` // vLLM
			TopProvider   struct {
				ContextLength       int `json:"context_length"`
				MaxCompletionTokens int `json:"max_completion_tokens"`
			} `json:"top_provider"`
			Pricing map[string]json.RawMessage `json:"pricing"` // OpenRouter: USD per token, as strings
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("models: bad response: %w", err)
	}
	var models []llm.ModelInfo
	for _, m := range out.Data {
		if m.ID == "" {
			continue
		}
		mi := llm.ModelInfo{ID: m.ID, Name: m.Name, ContextWindow: m.ContextLength, MaxOutput: m.TopProvider.MaxCompletionTokens}
		if mi.ContextWindow == 0 {
			mi.ContextWindow = m.TopProvider.ContextLength
		}
		if mi.ContextWindow == 0 {
			mi.ContextWindow = m.MaxModelLen
		}
		if p, ok := parsePricing(m.Pricing); ok {
			mi.Price = &p
		}
		models = append(models, mi)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

// parsePricing reads OpenRouter's per-token USD strings into a per-million
// Price. It reports false when no prompt or completion price is present.
func parsePricing(pricing map[string]json.RawMessage) (llm.Price, bool) {
	get := func(k string) (float64, bool) {
		r, ok := pricing[k]
		if !ok {
			return 0, false
		}
		var s string
		var f float64
		switch {
		case json.Unmarshal(r, &s) == nil:
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return 0, false
			}
			f = v
		case json.Unmarshal(r, &f) == nil:
		default:
			return 0, false
		}
		if f < 0 {
			return 0, false
		}
		return math.Round(f*1e6*1e6) / 1e6, true // per million, 6 decimals
	}
	in, okIn := get("prompt")
	out, okOut := get("completion")
	if !okIn && !okOut {
		return llm.Price{}, false
	}
	p := llm.Price{Input: in, Output: out}
	p.CacheRead, _ = get("input_cache_read")
	p.CacheWrite, _ = get("input_cache_write")
	return p, true
}
