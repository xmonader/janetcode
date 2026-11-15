package opencode

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

type modelKey struct{}
type transport struct {
	base          http.RoundTripper
	name, session string
}

var resetText = regexp.MustCompile(`(?i)resets?\s+in\s+([^."\n]+)`)
var resetUnits = regexp.MustCompile(`(?i)(\d+)\s*(hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)\b`)

func (p *Provider) httpClient() *http.Client {
	hc := &http.Client{}
	if p.cfg.HTTPClient != nil {
		*hc = *p.cfg.HTTPClient
	}
	base := hc.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	hc.Transport = &transport{base: base, name: p.cfg.Name, session: p.session}
	return hc
}

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	id := llm.SessionID(r.Context())
	if id == "" {
		id = t.session
	}
	r.Header.Set("x-opencode-session", id)
	r.Header.Set("User-Agent", "janetcode/opencode")
	resp, err := t.base.RoundTrip(r)
	if err != nil || resp == nil || (resp.StatusCode != 429 && resp.StatusCode != 403 && resp.StatusCode != 402) {
		return resp, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	text, message := strings.ToLower(string(raw)), ""
	if resp.StatusCode == 402 {
		message = t.name + " insufficient balance; top up at https://opencode.ai/zen or select opencode-go"
	} else {
		window := strings.Contains(text, "usage limit") || strings.Contains(text, "usagelimit") || strings.Contains(text, "subscription limit") || strings.Contains(text, "monthly") || strings.Contains(text, "weekly")
		burst := strings.Contains(text, "per minute") || strings.Contains(text, "per second") || strings.Contains(text, "requests per") || strings.Contains(text, "too many requests")
		reset := resetAfter(resp.Header.Get("Retry-After"), raw)
		if window && !burst && (reset == 0 || reset >= 2*time.Minute) {
			model, _ := r.Context().Value(modelKey{}).(string)
			message = t.name + " usage limit reached for " + model + "; switch model or wait for the plan window to reset"
			if reset > 0 {
				message += " (resets in " + reset.Round(time.Second).String() + ")"
			}
		}
	}
	if message != "" {
		// All protocol clients share a retry policy that recognizes this
		// quota wording and will not wait on an exhausted plan window.
		raw, _ = json.Marshal(map[string]any{"error": map[string]string{"type": "insufficient_quota", "message": message}})
		resp.Header.Set("Content-Type", "application/json")
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	return resp, nil
}

func resetAfter(header string, raw []byte) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 32); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && time.Until(at) > 0 {
		return time.Until(at)
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return 0
	}
	e, _ := body["error"].(map[string]any)
	for _, obj := range []map[string]any{body, e} {
		for _, key := range []string{"resets_in", "reset_in", "retry_after"} {
			if seconds, ok := obj[key].(float64); ok && seconds > 0 && seconds <= 365*24*3600 {
				return time.Duration(seconds) * time.Second
			}
		}
		for _, key := range []string{"resetAt", "reset_at"} {
			if value, ok := obj[key].(string); ok {
				if at, err := time.Parse(time.RFC3339, value); err == nil && time.Until(at) > 0 {
					return time.Until(at)
				}
			}
		}
	}
	if m := resetText.FindSubmatch(raw); len(m) > 1 {
		var total time.Duration
		for _, unit := range resetUnits.FindAllStringSubmatch(string(m[1]), -1) {
			n, err := strconv.ParseInt(unit[1], 10, 32)
			if err != nil {
				continue
			}
			scale := time.Second
			switch strings.ToLower(unit[2])[0] {
			case 'h':
				scale = time.Hour
			case 'm':
				scale = time.Minute
			}
			const maxReset = 365 * 24 * time.Hour
			if n > int64(maxReset/scale) {
				continue
			}
			if time.Duration(n)*scale > maxReset-total {
				return maxReset
			}
			total += time.Duration(n) * scale
		}
		if total > 0 {
			return total
		}
	}
	return 0
}
