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

// checkKey asks OpenRouter whether the key is good. Its /models is
// public, so listing models said nothing about the key: a mistyped one
// sailed through /connect and failed on the first prompt instead. GET
// /key needs a valid key and costs nothing. Other endpoints authenticate
// /models itself, so there is nothing to add for them.
func (p *Provider) checkKey(ctx context.Context) error {
	k, _ := p.ring.Current()
	if k == "" || !strings.Contains(strings.ToLower(p.cfg.BaseURL), "openrouter.ai") {
		return nil
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/key", nil)
	if err != nil {
		return err
	}
	hreq.Header.Set("Authorization", "Bearer "+k)
	resp, err := p.http.Do(hreq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &APIError{Status: resp.StatusCode, Message: "the API key was not accepted"}
	}
	// Anything else (a 404 if the route moves, a 5xx) is not evidence
	// against the key; listing goes ahead and the first call will tell.
	return nil
}

// ListModels implements llm.ModelLister via GET /models.
func (p *Provider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if err := p.checkKey(ctx); err != nil {
		return nil, err
	}
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
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, llm.MaxErrorBodyRead))
	if resp.StatusCode/100 != 2 {
		msg := llm.SafeErrorMessage(resp.StatusCode, strings.TrimSpace(string(raw)))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		var env struct {
			Error *wireError `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil && env.Error.Message != "" {
			if m := llm.SafeErrorMessage(resp.StatusCode, env.Error.Message); m != "" {
				msg = m
			}
		}
		return nil, &APIError{Status: resp.StatusCode, Message: msg}
	}
	if readErr != nil {
		return nil, fmt.Errorf("models: read response: %w", readErr)
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
	// get reports a price as present-and-usable, absent, or
	// present-but-unusable — a distinction the caller needs: a missing
	// side is priced at zero (older listings omit one), but a side whose
	// value math cannot use (NaN, ±Inf from a hand-rolled endpoint,
	// negative) must poison the whole entry, or it would silently read
	// as free tokens.
	get := func(k string) (v float64, ok, bad bool) {
		r, present := pricing[k]
		if !present {
			return 0, false, false
		}
		var s string
		var f float64
		switch {
		case json.Unmarshal(r, &s) == nil:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return 0, false, true
			}
			f = parsed
		case json.Unmarshal(r, &f) == nil:
		default:
			return 0, false, true
		}
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false, true
		}
		return math.Round(f*1e6*1e6) / 1e6, true, false // per million, 6 decimals
	}
	in, okIn, badIn := get("prompt")
	out, okOut, badOut := get("completion")
	if badIn || badOut {
		return llm.Price{}, false
	}
	if !okIn && !okOut {
		return llm.Price{}, false
	}
	p := llm.Price{Input: in, Output: out}
	// Cache prices are optional; an unusable one prices that cache side
	// at zero rather than poisoning the entry, matching a missing key.
	if v, ok, bad := get("input_cache_read"); ok || bad {
		p.CacheRead = v
	}
	if v, ok, bad := get("input_cache_write"); ok || bad {
		p.CacheWrite = v
	}
	return p, true
}
