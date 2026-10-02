package openai

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The two streams the ai02 broker sent for one request on 2026-10-02:
// the honest route says "provider" on every chunk and mints vLLM ids; the
// Kimi-served name echoes the requested model and mints Kimi's tool_ ids.
// What the response said about itself is what a provenance check needs.
func TestStreamRecordsWhoServedIt(t *testing.T) {
	honest := sse(
		`{"id":"chatcmpl-a28f6886ca940980","model":"XiaomiMiMo/MiMo-V2.6-Flash-RL","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"chatcmpl-tool-bebdb548a1cfdcf0","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}],"provider":"gpu_octo"}`,
		`{"id":"chatcmpl-a28f6886ca940980","model":"XiaomiMiMo/MiMo-V2.6-Flash-RL","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":65,"completion_tokens":23},"provider":"gpu_octo"}`,
	)
	resp, err := readStream(strings.NewReader(honest), nil, func(llm.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Provenance{Model: "XiaomiMiMo/MiMo-V2.6-Flash-RL", Provider: "gpu_octo", IDPrefix: "chatcmpl-", ToolIDs: "chatcmpl-tool-"}
	if resp.Served != want {
		t.Fatalf("served = %+v, want %+v", resp.Served, want)
	}

	swapped := sse(
		`{"id":"chatcmpl-0fVnraQ9sUZtZmXDKFixZZG4","model":"xiaomi/mimo-v2.6-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tool_SYpvQ6ey6IiYaPSaB6rEppQp","type":"function","function":{"name":"get_time","arguments":""}}]}}]}`,
		`{"id":"chatcmpl-0fVnraQ9sUZtZmXDKFixZZG4","model":"xiaomi/mimo-v2.6-flash","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{}"}}]}}]}`,
		`{"id":"chatcmpl-0fVnraQ9sUZtZmXDKFixZZG4","model":"xiaomi/mimo-v2.6-flash","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	resp, err = readStream(strings.NewReader(swapped), nil, func(llm.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Served.ToolIDs != "tool_24" || resp.Served.Model != "xiaomi/mimo-v2.6-flash" || resp.Served.Provider != "" {
		t.Fatalf("served = %+v", resp.Served)
	}

	// Non-streamed (or a last chunk carrying it), the broker says it.
	told := sse(`{"id":"gen-1","model":"anthropic/claude-haiku-4.5","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}],"x_aibroker":{"provider":"kimi","provider_model":"anthropic/claude-haiku-4.5"}}`)
	resp, err = readStream(strings.NewReader(told), nil, func(llm.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Served.Provider != "kimi" || resp.Served.ProviderModel != "anthropic/claude-haiku-4.5" || resp.Served.IDPrefix != "gen-" {
		t.Fatalf("served = %+v", resp.Served)
	}
}
