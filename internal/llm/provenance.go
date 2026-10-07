package llm

import (
	"regexp"
	"strings"
)

// Provenance is what a response said about who actually served it. A
// gateway is free to answer one model name with another model — on
// 2026-10-01 the ai02 broker answered every name, Claude included, with
// Kimi for hours, and an A/B measured the swap instead of the prompt
// (docs/benchmarks/jit/REPORT.md). Nothing in the answer text says so;
// these fields are the evidence that does. Each is short and most are
// empty for a direct provider, so it can ride on every model call's
// event without bloating the log.
type Provenance struct {
	// Model is the response's own model field. Gateways often echo the
	// requested name here whatever served it, so it proves little on
	// its own when it agrees.
	Model string `json:"model,omitempty"`
	// Provider is a gateway's statement of the upstream that served the
	// call: OpenRouter's and the ai02 broker's "provider" field, or the
	// broker's x_aibroker.provider. It names a vendor ("Anthropic",
	// "kimi") or only a host ("gpu_octo", "Amazon Bedrock").
	Provider string `json:"provider,omitempty"`
	// ProviderModel is the upstream's own name for the model, when the
	// gateway reports it (x_aibroker.provider_model).
	ProviderModel string `json:"provider_model,omitempty"`
	// IDPrefix is the response id's prefix ("chatcmpl-", "gen-", "msg_"),
	// not the id: the shape says which server built the response, the
	// value says nothing anyone needs.
	IDPrefix string `json:"id,omitempty"`
	// ToolIDs is the shape of the tool-call ids the server minted (see
	// ToolIDFormat), the one signal a gateway that rewrites everything
	// else rarely touches: Anthropic's are toolu_…, Kimi's tool_ and 24
	// characters, vLLM's chatcmpl-tool-….
	ToolIDs string `json:"tool_ids,omitempty"`
}

// Empty reports whether the response said nothing about its origin.
func (p Provenance) Empty() bool { return p == Provenance{} }

var (
	kimiToolID     = regexp.MustCompile(`^tool_[A-Za-z0-9]{24}$`)
	kimiNativeID   = regexp.MustCompile(`^functions\.[A-Za-z0-9_.\-]+:\d+$`)
	openaiToolID   = regexp.MustCompile(`^call_[A-Za-z0-9]{24}$`)
	hexCallToolID  = regexp.MustCompile(`^call_[0-9a-f]{20,}$`)
	mistralToolID  = regexp.MustCompile(`^[A-Za-z0-9]{9}$`)
	idPrefixSplitR = regexp.MustCompile(`^[A-Za-z]+[-_]`)
)

// ToolIDFormat classifies one tool-call id by shape. The classes are
// coarse on purpose: the value of an id is random, its shape is the
// server's habit. A shape janetcode does not know comes back as its
// leading word and separator ("fc_", "id-"), or "other".
func ToolIDFormat(id string) string {
	switch {
	case id == "":
		return ""
	case strings.HasPrefix(id, "toolu_bdrk_"):
		return "toolu_bdrk_"
	case strings.HasPrefix(id, "toolu_vrtx_"):
		return "toolu_vrtx_"
	case strings.HasPrefix(id, "toolu_"):
		return "toolu_"
	case strings.HasPrefix(id, "chatcmpl-tool-"):
		return "chatcmpl-tool-"
	case kimiToolID.MatchString(id):
		return "tool_24"
	case kimiNativeID.MatchString(id):
		return "functions.name:n"
	case strings.HasPrefix(id, "call_00_"):
		return "call_00_"
	case hexCallToolID.MatchString(id):
		// Before call_24: an all-hex id of 24 is a different server's.
		return "call_hex"
	case openaiToolID.MatchString(id):
		return "call_24"
	case mistralToolID.MatchString(id):
		return "alnum9"
	}
	if m := idPrefixSplitR.FindString(id); m != "" && len(m) <= 16 {
		return m
	}
	return "other"
}

// IDPrefix is the leading word of a response id with its separator
// ("chatcmpl-", "gen-", "msg_"), or "" when there is none.
func IDPrefix(id string) string {
	m := idPrefixSplitR.FindString(id)
	if len(m) > 16 {
		return ""
	}
	return m
}

// ToolIDsOf is the tool-id format of a message's calls: the format of
// the first call, which is the server's — a later call in the same
// message comes from the same place.
func ToolIDsOf(m Message) string {
	for _, b := range m.Blocks {
		if b.Type == BlockToolUse && b.ID != "" {
			return ToolIDFormat(b.ID)
		}
	}
	return ""
}
