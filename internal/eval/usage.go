package eval

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ParseUsageJSON extracts reported usage from a JSON object, array, or NDJSON stream.
// Missing fields stay unset. Values are never estimated.
func ParseUsageJSON(data []byte) UsageMetrics {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return UsageMetrics{}
	}
	if events := ndjsonEvents(data); len(events) > 1 {
		return parseEvents(events)
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return UsageMetrics{}
	}
	return parseValue(v)
}

func ndjsonEvents(data []byte) []any {
	lines := bytes.Split(data, []byte("\n"))
	var events []any
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' && line[0] != '[' {
			continue
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			continue
		}
		events = append(events, v)
	}
	return events
}

func parseEvents(events []any) UsageMetrics {
	var result *UsageMetrics
	var ends, assistants UsageMetrics
	var endTurns, asstTurns int
	for _, ev := range events {
		if isType(ev, "result") || isType(ev, "result_success") {
			u := parseValue(ev)
			if u.HasTokens() || u.ToolCalls.Set || u.Turns.Set {
				cp := u
				result = &cp
			}
		}
		u := nestedAssistantUsage(ev)
		if !u.HasTokens() {
			continue
		}
		// pi JSON mode repeats usage on every message_update; message_end
		// is one completed model call.
		switch {
		case isType(ev, "message_end"):
			ends = ends.Add(u)
			endTurns++
		case isType(ev, "assistant"):
			assistants = assistants.Add(u)
			asstTurns++
		}
	}
	if result != nil {
		return *result
	}
	if ends.HasTokens() {
		if !ends.Turns.Set {
			ends.Turns = Int(endTurns)
		}
		return ends
	}
	if assistants.HasTokens() {
		if !assistants.Turns.Set {
			assistants.Turns = Int(asstTurns)
		}
		return assistants
	}
	var last UsageMetrics
	for _, ev := range events {
		if u := directUsage(ev); u.HasTokens() {
			last = u
		}
	}
	return last
}

func nestedAssistantUsage(ev any) UsageMetrics {
	m := asMap(ev)
	if m == nil {
		return UsageMetrics{}
	}
	msg := asMap(m["message"])
	if msg == nil {
		msg = m
	}
	if role, _ := msg["role"].(string); role != "" && role != "assistant" {
		return UsageMetrics{}
	}
	if raw, ok := lookup(msg, "usage"); ok {
		return usageFromMap(asMap(raw))
	}
	return UsageMetrics{}
}

func parseValue(v any) UsageMetrics {
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			if isType(it, "result") {
				return parseValue(it)
			}
		}
		var sum UsageMetrics
		for _, it := range x {
			sum = sum.Add(directUsage(it))
		}
		if sum.HasTokens() {
			return sum
		}
		for _, it := range x {
			sum = sum.Add(parseValue(it))
		}
		return sum
	case map[string]any:
		if u := fromStats(x); u.HasTokens() || u.ToolCalls.Set {
			if d := directUsage(x); d.HasTokens() && !u.HasTokens() {
				u.Input = d.Input
				u.Output = d.Output
				u.CacheRead = d.CacheRead
				u.CacheWrite = d.CacheWrite
			}
			if t := pickInt(x, "num_turns", "numTurns", "turns"); t.Set && !u.Turns.Set {
				u.Turns = t
			}
			if c := pickCost(x); c.Set && !u.CostUSD.Set {
				u.CostUSD = c
			}
			return u
		}
		if u := directUsage(x); u.HasTokens() || u.ToolCalls.Set || u.Turns.Set || u.CostUSD.Set {
			return u
		}
		var sum UsageMetrics
		for _, child := range x {
			sum = sum.Add(parseValue(child))
		}
		return sum
	default:
		return UsageMetrics{}
	}
}

func isType(v any, want string) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	t, ok := m["type"].(string)
	return ok && strings.EqualFold(t, want)
}

func directUsage(v any) UsageMetrics {
	m, ok := v.(map[string]any)
	if !ok {
		return UsageMetrics{}
	}
	var u UsageMetrics
	if raw, ok := lookup(m, "usage"); ok {
		u = u.Add(usageFromMap(asMap(raw)))
	}
	u = u.Add(usageFromMap(m))
	if t := pickInt(m, "num_turns", "numTurns", "turns", "api_calls", "apiCalls"); t.Set && !u.Turns.Set {
		u.Turns = t
	}
	if c := pickCost(m); c.Set && !u.CostUSD.Set {
		u.CostUSD = c
	}
	if tc := pickInt(m, "tool_calls", "toolCalls", "totalCalls"); tc.Set && !u.ToolCalls.Set {
		u.ToolCalls = tc
	}
	return u
}

func fromStats(m map[string]any) UsageMetrics {
	stats := asMap(m["stats"])
	if stats == nil {
		if mu, ok := lookup(m, "modelUsage"); ok {
			return fromModelUsage(asMap(mu))
		}
		return UsageMetrics{}
	}
	var u UsageMetrics
	models := asMap(stats["models"])
	for _, mv := range models {
		mm := asMap(mv)
		tok := asMap(mm["tokens"])
		if tok == nil {
			u = u.Add(usageFromMap(mm))
			continue
		}
		u.Input = u.Input.Add(pickInt(tok, "prompt", "input", "uncached"))
		u.Output = u.Output.Add(pickInt(tok, "candidates", "output", "completion"))
		u.CacheRead = u.CacheRead.Add(pickInt(tok, "cached", "cache", "cacheRead"))
		u.CacheWrite = u.CacheWrite.Add(pickInt(tok, "cacheWrite", "cachedWrite"))
	}
	if tools := asMap(stats["tools"]); tools != nil {
		u.ToolCalls = u.ToolCalls.Add(pickInt(tools, "totalCalls", "total_calls", "total"))
	}
	return u
}

func fromModelUsage(m map[string]any) UsageMetrics {
	var u UsageMetrics
	for _, mv := range m {
		mm := asMap(mv)
		u = u.Add(usageFromMap(mm))
		u.Turns = u.Turns.Add(pickInt(mm, "modelCalls", "model_calls"))
		if c := pickCost(mm); c.Set {
			u.CostUSD = u.CostUSD.Add(c)
		}
	}
	return u
}

func usageFromMap(m map[string]any) UsageMetrics {
	if m == nil {
		return UsageMetrics{}
	}
	u := UsageMetrics{
		Input: pickInt(m,
			"input_tokens", "inputTokens", "prompt_tokens", "promptTokens",
			"promptTokenCount", "input"),
		Output: pickInt(m,
			"output_tokens", "outputTokens", "completion_tokens", "completionTokens",
			"candidatesTokenCount", "output"),
		CacheRead: pickInt(m,
			"cache_read_tokens", "cacheReadTokens", "cache_read_input_tokens",
			"cacheReadInputTokens", "cacheRead", "cached_tokens",
			"cachedContentTokenCount", "cached_content_token_count"),
		CacheWrite: pickInt(m,
			"cache_write_tokens", "cacheWriteTokens", "cache_creation_input_tokens",
			"cacheCreationInputTokens", "cacheWrite", "cache_creation_tokens"),
		ToolCalls: pickInt(m, "tool_calls", "toolCalls"),
		Turns:     pickInt(m, "turns", "num_turns", "numTurns"),
		CostUSD:   pickCost(m),
	}
	// "input" on a non-usage object (paths, etc.) is not tokens. Require a
	// sibling output/token field before accepting a bare "input" integer.
	if u.Input.Set && !u.Output.Set && !u.CacheRead.Set && !u.CacheWrite.Set {
		if _, ok := lookup(m, "input_tokens"); !ok {
			if _, ok := lookup(m, "inputTokens"); !ok {
				if _, ok := lookup(m, "prompt_tokens"); !ok {
					if _, ok := lookup(m, "promptTokens"); !ok {
						u.Input = OptInt{}
					}
				}
			}
		}
		if _, has := lookup(m, "input"); has && !u.Input.Set {
			if _, ok := lookup(m, "output"); ok {
				u.Input = pickInt(m, "input")
				u.Output = pickInt(m, "output")
			}
		}
	}
	return u
}

func pickInt(m map[string]any, keys ...string) OptInt {
	if m == nil {
		return OptInt{}
	}
	for _, k := range keys {
		if v, ok := lookup(m, k); ok {
			if n, ok := asInt(v); ok {
				return Int(n)
			}
		}
	}
	return OptInt{}
}

func pickCost(m map[string]any) OptFloat {
	if m == nil {
		return OptFloat{}
	}
	for _, k := range []string{"cost_usd", "costUSD", "total_cost_usd", "totalCostUsd", "cost"} {
		v, ok := lookup(m, k)
		if !ok {
			continue
		}
		if sub := asMap(v); sub != nil {
			if f := pickCost(sub); f.Set {
				return f
			}
			if n, ok := asFloat(sub["total"]); ok {
				return Float(n)
			}
			continue
		}
		if n, ok := asFloat(v); ok {
			return Float(n)
		}
	}
	return OptFloat{}
}

func lookup(m map[string]any, key string) (any, bool) {
	if v, ok := m[key]; ok {
		return v, true
	}
	want := canonKey(key)
	for k, v := range m {
		if canonKey(k) == want {
			return v, true
		}
	}
	return nil, false
}

func canonKey(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	return s
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			f, ferr := n.Float64()
			return int(f), ferr == nil
		}
		return int(i), true
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(n), ",", "")
		i, err := strconv.Atoi(s)
		return i, err == nil
	default:
		return 0, false
	}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		s := strings.ReplaceAll(strings.TrimSpace(n), ",", "")
		f, err := strconv.ParseFloat(s, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

var (
	usageLine = regexp.MustCompile(`(?i)(input tokens|output tokens|prompt tokens(?: \(total\))?|completion tokens|cache read(?: tokens)?|cache write(?: tokens)?|cache_read_tokens|cache_write_tokens|prompt_tokens|completion_tokens|api calls|tool calls)\s*[:]\s*([0-9,]+)`)
	// hermes-agent verbose: "API call #4: ... in=3741 out=64 total=3805 ... cache=2048/3741 (55%)"
	hermesAPICall = regexp.MustCompile(`(?m)API call #\d+:[^\n]*\bin=(\d+)\s+out=(\d+)\b(?:[^\n]*\bcache=(\d+)/(\d+))?`)
	// hermes-agent debug: "Token usage: prompt=3,741, completion=64, total=3,805"
	hermesTokenUsage = regexp.MustCompile(`(?i)Token usage:\s*prompt=([0-9,]+),\s*completion=([0-9,]+)`)
)

func atoiComma(s string) int {
	n, _ := strconv.Atoi(strings.ReplaceAll(s, ",", ""))
	return n
}

// ParseUsageText scrapes hermes-style usage lines. Per-call rows are summed;
// a field the log never emitted stays unset.
func ParseUsageText(s string) UsageMetrics {
	if u := parseHermesAPICalls(s); u.HasTokens() {
		return u
	}
	if u := parseHermesTokenUsage(s); u.HasTokens() {
		return u
	}
	var u UsageMetrics
	for _, m := range usageLine.FindAllStringSubmatch(s, -1) {
		n, err := strconv.Atoi(strings.ReplaceAll(m[2], ",", ""))
		if err != nil {
			continue
		}
		label := strings.ToLower(m[1])
		switch {
		case strings.HasPrefix(label, "input token"):
			u.Input = Int(n)
		case strings.HasPrefix(label, "output token"):
			u.Output = Int(n)
		case strings.HasPrefix(label, "prompt token"):
			if !u.Input.Set {
				u.Input = Int(n)
			}
		case strings.HasPrefix(label, "completion token"):
			if !u.Output.Set {
				u.Output = Int(n)
			}
		case strings.Contains(label, "cache read"):
			u.CacheRead = Int(n)
		case strings.Contains(label, "cache write"):
			u.CacheWrite = Int(n)
		case strings.Contains(label, "api call"):
			u.Turns = Int(n)
		case strings.Contains(label, "tool call"):
			u.ToolCalls = Int(n)
		}
	}
	if t := regexp.MustCompile(`(?m)API Calls:\s*(\d+)`).FindStringSubmatch(s); len(t) == 2 && !u.Turns.Set {
		n, _ := strconv.Atoi(t[1])
		u.Turns = Int(n)
	}
	return u
}

func parseHermesAPICalls(s string) UsageMetrics {
	var u UsageMetrics
	n := 0
	for _, m := range hermesAPICall.FindAllStringSubmatch(s, -1) {
		u.Input = u.Input.Add(Int(atoiComma(m[1])))
		u.Output = u.Output.Add(Int(atoiComma(m[2])))
		if m[3] != "" {
			u.CacheRead = u.CacheRead.Add(Int(atoiComma(m[3])))
		}
		n++
	}
	if n > 0 {
		u.Turns = Int(n)
	}
	return u
}

func parseHermesTokenUsage(s string) UsageMetrics {
	var u UsageMetrics
	n := 0
	for _, m := range hermesTokenUsage.FindAllStringSubmatch(s, -1) {
		u.Input = u.Input.Add(Int(atoiComma(m[1])))
		u.Output = u.Output.Add(Int(atoiComma(m[2])))
		n++
	}
	if n > 0 {
		u.Turns = Int(n)
	}
	return u
}

// ExtractAssistantText pulls the model's final text out of a CLI transcript
// so EVAL_OUTPUT is not the raw JSON (which can poison filename checks).
func ExtractAssistantText(stdout []byte) string {
	s := strings.TrimSpace(string(stdout))
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "FINAL RESPONSE:"); i >= 0 {
		rest := s[i+len("FINAL RESPONSE:"):]
		rest = strings.TrimLeft(rest, "\n- \t")
		if j := strings.Index(rest, "\n👋"); j >= 0 {
			rest = rest[:j]
		}
		if t := strings.TrimSpace(rest); t != "" {
			return t
		}
	}
	if events := ndjsonEvents(stdout); len(events) > 0 {
		if t := textFromEvents(events); t != "" {
			return t
		}
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(stdout)))
	if err := dec.Decode(&v); err == nil {
		if t := textFromValue(v); t != "" {
			return t
		}
	}
	return s
}

func textFromEvents(events []any) string {
	var last string
	for _, ev := range events {
		if isType(ev, "result") {
			if t := textFromValue(ev); t != "" {
				return t
			}
		}
		if t := assistantText(ev); t != "" {
			last = t
		}
	}
	return last
}

func textFromValue(v any) string {
	switch x := v.(type) {
	case []any:
		for i := len(x) - 1; i >= 0; i-- {
			if isType(x[i], "result") {
				if t := textFromValue(x[i]); t != "" {
					return t
				}
			}
		}
		var last string
		for _, it := range x {
			if t := assistantText(it); t != "" {
				last = t
			}
		}
		return last
	case map[string]any:
		if isType(x, "result") {
			for _, k := range []string{"result", "response", "text"} {
				if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
					return strings.TrimSpace(s)
				}
			}
		}
		for _, k := range []string{"text", "response", "final_response", "finalResponse"} {
			if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		if t := assistantText(x); t != "" {
			return t
		}
		return ""
	default:
		return ""
	}
}

func assistantText(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	if msg := asMap(m["message"]); msg != nil {
		if role, _ := msg["role"].(string); role == "assistant" || role == "" {
			if t := blocksText(msg["content"]); t != "" {
				return t
			}
		}
	}
	if t := blocksText(m["content"]); t != "" {
		if role, _ := m["role"].(string); role == "assistant" || role == "" {
			if isType(m, "assistant") || role == "assistant" || m["type"] == "assistant" {
				return t
			}
		}
	}
	if isType(m, "assistant") || m["role"] == "assistant" {
		if s, ok := m["text"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	if ev := asMap(m["event"]); ev != nil {
		return assistantText(ev)
	}
	return ""
}

func blocksText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		var b strings.Builder
		for _, it := range x {
			m := asMap(it)
			if m == nil {
				continue
			}
			typ, _ := m["type"].(string)
			if typ == "text" || typ == "" {
				if s, ok := m["text"].(string); ok && s != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(s)
				}
			}
		}
		return strings.TrimSpace(b.String())
	default:
		return ""
	}
}
