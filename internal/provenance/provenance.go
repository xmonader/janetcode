// Package provenance decides whether a model call was served by the
// model that was asked for.
//
// A gateway is free to answer one model name with another model, and
// nothing in the answer text says so. On 2026-10-01 the ai02 broker
// answered every name — Claude, DeepSeek, Qwen, MiMo — with Kimi for
// hours; an A/B that ran through it measured the swap instead of the
// prompt and was published before anyone noticed
// (docs/benchmarks/jit/REPORT.md). The check here reads what a response
// says about itself (llm.Provenance) and compares vendors, not names:
// gateways rename models legitimately ("anthropic/claude-sonnet-4.5" for
// "claude-sonnet-4-5"), and a host is not a vendor (Bedrock serves Claude,
// "gpu_octo" serves MiMo), so only a signal that names a different
// *vendor* is a mismatch.
package provenance

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
)

// vendors maps a vendor to the words that identify it in a model or
// provider name, most specific first. The order of the slice matters: a
// name is given the first vendor any of whose words it contains.
var vendors = []struct {
	name  string
	words []string
}{
	{"anthropic", []string{"anthropic", "claude"}},
	{"moonshot", []string{"moonshot", "kimi"}},
	{"deepseek", []string{"deepseek"}},
	{"xiaomi", []string{"xiaomi", "mimo"}},
	{"qwen", []string{"qwen", "qwq", "alibaba", "dashscope", "tongyi"}},
	{"zhipu", []string{"z-ai", "zhipu", "chatglm", "glm-", "glm4", "glm5"}},
	{"minimax", []string{"minimax"}},
	{"mistral", []string{"mistral", "codestral", "devstral", "ministral", "magistral", "voxtral", "pixtral"}},
	{"google", []string{"gemini", "gemma", "google"}},
	{"meta", []string{"llama", "muse-spark", "meta-llama", "meta/"}},
	{"xai", []string{"grok", "x-ai", "xai"}},
	{"openai", []string{"openai", "gpt-", "gpt5", "chatgpt", "codex", "o1-", "o3-", "o4-"}},
	{"cohere", []string{"cohere", "command-r", "command-a"}},
	{"nvidia", []string{"nemotron", "nvidia"}},
	{"inception", []string{"mercury", "inception"}},
}

// hosts are words in a provider name that make it a host, not a vendor:
// "Amazon Bedrock" serves Claude, "Google Vertex" serves Claude and
// Gemini, "gpu_octo" serves whatever was deployed on it. A provider name
// with one of these never counts as evidence about the vendor.
var hosts = []string{
	"bedrock", "vertex", "azure", "together", "fireworks", "deepinfra", "novita",
	"groq", "cerebras", "sambanova", "lambda", "hyperbolic", "nebius", "parasail",
	"baseten", "friendli", "chutes", "siliconflow", "kluster", "openrouter",
	"gpu_", "gpu-", "local", "ollama", "vllm", "llama.cpp", "lmstudio", "self",
}

// Vendor is the vendor a model or provider name identifies, or "" when
// it identifies none (an auto-router, a host, a name janetcode does not
// know). For "vendor/model" names the vendor part is tried first, so
// "deepseek/deepseek-r1-distill-qwen" is DeepSeek's.
func Vendor(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return ""
	}
	if i := strings.IndexByte(n, '/'); i > 0 {
		if v := vendorOf(n[:i+1]); v != "" {
			return v
		}
		n = n[i+1:]
	}
	return vendorOf(n)
}

func vendorOf(n string) string {
	for _, v := range vendors {
		for _, w := range v.words {
			if strings.Contains(n, w) {
				return v.name
			}
		}
	}
	// The o-series are only "o1", "o3", "o4-mini": whole names, too short
	// to match as substrings safely.
	switch strings.TrimSuffix(n, "/") {
	case "o1", "o3", "o4", "o1-pro", "o3-pro":
		return "openai"
	}
	return ""
}

// providerVendor is Vendor for a gateway's provider field, which names a
// host as often as a vendor.
func providerVendor(p string) string {
	l := strings.ToLower(p)
	for _, h := range hosts {
		if strings.Contains(l, h) {
			return ""
		}
	}
	return Vendor(p)
}

// toolIDVendor is the vendor a tool-call id format proves. Only shapes
// one vendor uses are listed: toolu_ ids are Anthropic's on every route
// that serves Claude, a tool_ id of exactly 24 characters and the
// functions.name:n form are Kimi's. Generic shapes (call_…, chatcmpl-
// tool-…) are recorded but prove nothing.
func toolIDVendor(format string) string {
	switch format {
	case "toolu_", "toolu_bdrk_", "toolu_vrtx_":
		return "anthropic"
	case "tool_24", "functions.name:n":
		return "moonshot"
	}
	return ""
}

// Verdict is the outcome of checking one call.
type Verdict struct {
	// Mismatch is set when the response named a vendor other than the
	// requested model's.
	Mismatch bool
	// Requested and Served are the two vendors; Served is "" when the
	// response said nothing that names one.
	Requested, Served string
	// Evidence is the field that decided it, for a notice a person can
	// check: `provider "kimi"`, `tool ids tool_24`.
	Evidence string
}

// String is the one-line notice: "requested deepseek/… (deepseek), served
// by moonshot: tool ids tool_24".
func (v Verdict) String() string {
	if !v.Mismatch {
		return ""
	}
	return fmt.Sprintf("served by %s, not %s (%s)", v.Served, v.Requested, v.Evidence)
}

// Check compares the requested model with what the response said about
// itself. Signals, strongest first: a gateway's provider field naming a
// vendor, the upstream's own model name, the response's model field, and
// a vendor-specific tool-id shape. An unknown requested vendor (an
// auto-router, a private name) gives no verdict: there is nothing to
// disagree with.
func Check(requested string, p llm.Provenance) Verdict {
	req := Vendor(requested)
	v := Verdict{Requested: req}
	if req == "" {
		return v
	}
	try := func(vendor, evidence string) bool {
		if vendor == "" {
			return false
		}
		v.Served = vendor
		if vendor != req {
			v.Mismatch, v.Evidence = true, evidence
			return true
		}
		return false
	}
	if try(providerVendor(p.Provider), fmt.Sprintf("provider %q", p.Provider)) {
		return v
	}
	if try(Vendor(p.ProviderModel), fmt.Sprintf("upstream model %q", p.ProviderModel)) {
		return v
	}
	if try(Vendor(p.Model), fmt.Sprintf("model %q", p.Model)) {
		return v
	}
	if try(toolIDVendor(p.ToolIDs), "tool ids "+p.ToolIDs) {
		return v
	}
	return v
}

var dateSuffix = regexp.MustCompile(`([-@](20\d{2}-?\d{2}-?\d{2}|latest|v\d+(:\d+)?))+$`)

// SameModel reports whether a served model name is the requested model
// under another spelling: a vendor prefix ("anthropic/claude-…"), a
// dated snapshot ("…-20250929", "…@20250929"), "." for "-" in a version.
// It is for display (/stats "served elsewhere"), not the vendor check.
func SameModel(requested, served string) bool {
	return canon(requested) == canon(served)
}

func canon(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s = dateSuffix.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, ".", "-")
	return strings.TrimSuffix(s, ":free")
}
