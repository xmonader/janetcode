package provenance

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestVendor(t *testing.T) {
	for name, want := range map[string]string{
		"xiaomi/mimo-v2.6-flash":            "xiaomi",
		"XiaomiMiMo/MiMo-V2.6-Flash-RL":     "xiaomi",
		"Qwen/Qwen3.8-27B-FP8":              "qwen",
		"qwen3.7-plus":                      "qwen",
		"deepseek-ai/DeepSeek-V4.1-Flash":   "deepseek",
		"deepseek/deepseek-r1-distill-qwen": "deepseek",
		"anthropic/claude-haiku-4.5":        "anthropic",
		"claude-sonnet-4-6":                 "anthropic",
		"kimi-k2.7":                         "moonshot",
		"moonshotai/kimi-k3":                "moonshot",
		"openai/gpt-5.4-mini":               "openai",
		"o3":                                "openai",
		"z-ai/glm-5.3":                      "zhipu",
		"meta/muse-spark-1.3":               "meta",
		"openrouter/auto":                   "",
		"m":                                 "",
	} {
		if got := Vendor(name); got != want {
			t.Errorf("Vendor(%q) = %q, want %q", name, got, want)
		}
	}
}

// The incident of 2026-10-01, as the broker actually streamed it: the
// model field echoes the requested name, there is no provider field, and
// only the tool-call ids are Kimi's.
func TestCheckCatchesTheBrokerSwap(t *testing.T) {
	v := Check("xiaomi/mimo-v2.6-flash", llm.Provenance{Model: "xiaomi/mimo-v2.6-flash", IDPrefix: "chatcmpl-", ToolIDs: llm.ToolIDFormat("tool_SYpvQ6ey6IiYaPSaB6rEppQp")})
	if !v.Mismatch || v.Served != "moonshot" || v.Requested != "xiaomi" {
		t.Fatalf("verdict = %+v", v)
	}
	// Non-streamed, the broker says it outright.
	v = Check("anthropic/claude-haiku-4.5", llm.Provenance{Model: "anthropic/claude-haiku-4.5", Provider: "kimi", ProviderModel: "anthropic/claude-haiku-4.5"})
	if !v.Mismatch || v.Served != "moonshot" || v.Evidence != `provider "kimi"` {
		t.Fatalf("verdict = %+v", v)
	}
}

// The honest routes of the same broker, and the legitimate ways a
// gateway renames or hosts a model, are not mismatches.
func TestCheckPassesHonestRoutes(t *testing.T) {
	for _, c := range []struct {
		req string
		p   llm.Provenance
	}{
		{"XiaomiMiMo/MiMo-V2.6-Flash-RL", llm.Provenance{Model: "XiaomiMiMo/MiMo-V2.6-Flash-RL", Provider: "gpu_octo", ToolIDs: llm.ToolIDFormat("chatcmpl-tool-bebdb548a1cfdcf0")}},
		{"Qwen/Qwen3.8-27B-FP8", llm.Provenance{Model: "Qwen/Qwen3.8-27B-FP8", Provider: "gpu_vast", ToolIDs: llm.ToolIDFormat("call_8cf10c3a9e2b4d5f6a7b8c9d")}},
		{"claude-sonnet-4-5", llm.Provenance{Model: "anthropic/claude-sonnet-4.5", Provider: "Amazon Bedrock", ToolIDs: "toolu_bdrk_"}},
		{"claude-sonnet-4-6", llm.Provenance{Model: "claude-sonnet-4-6-20260101", IDPrefix: "msg_", ToolIDs: "toolu_"}},
		{"kimi-k2.7", llm.Provenance{Provider: "kimi", ToolIDs: "tool_24"}},
		{"openrouter/auto", llm.Provenance{Model: "anthropic/claude-opus-5", Provider: "Anthropic"}},
		{"deepseek/deepseek-v4.1-flash", llm.Provenance{}},
	} {
		if v := Check(c.req, c.p); v.Mismatch {
			t.Errorf("Check(%q, %+v) = mismatch %+v", c.req, c.p, v)
		}
	}
}

func TestToolIDFormat(t *testing.T) {
	for id, want := range map[string]string{
		"toolu_01HT1PeQFvZPHTHvGjATNyPN":      "toolu_",
		"toolu_bdrk_016dXtk8Q2FWJGra9SKBQCte": "toolu_bdrk_",
		"tool_sXslgABCDEFGHIJKLMNOPQRS":       "tool_24",
		"functions.get_time:0":                "functions.name:n",
		"call_00_abc":                         "call_00_",
		"call_01a0eebf275d74d7b4e13cfe":       "call_hex",
		"chatcmpl-tool-bebdb548a1cfdcf0":      "chatcmpl-tool-",
		"":                                    "",
	} {
		if got := llm.ToolIDFormat(id); got != want {
			t.Errorf("ToolIDFormat(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestSameModel(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"claude-sonnet-4-6", "claude-sonnet-4-6-20260101", true},
		{"claude-sonnet-4-5", "anthropic/claude-sonnet-4.5", true},
		{"claude-opus-4-5", "claude-opus-4-5@20251101", true},
		{"qwen/qwen3.8-27b", "qwen/qwen3.8-27b:free", true},
		{"claude-sonnet-4-6", "claude-haiku-4-5", false},
		{"deepseek-v4-flash", "deepseek-v4-pro", false},
	} {
		if got := SameModel(c.a, c.b); got != c.want {
			t.Errorf("SameModel(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
