package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A gateway that answers a model name with another vendor's model is
// said once per session, counted on every call, and written on the
// assistant events — the 2026-10-01 broker swap, which went unnoticed
// through a whole A/B, would have been on the first line of the run.
func TestServedByAnotherVendorIsSaidOnceAndCounted(t *testing.T) {
	kimi := llm.Provenance{Model: "xiaomi/mimo-v2.6-flash", IDPrefix: "chatcmpl-", ToolIDs: "tool_24"}
	r1 := toolCall("tool_SYpvQ6ey6IiYaPSaB6rEppQp", "noop", `{}`)
	r1.Served = kimi
	r2 := text("done")
	r2.Served = kimi
	f := &fake{responses: []*llm.Response{r1, r2}}
	log := &session.Memory{}
	a := &Agent{Provider: f, Model: "xiaomi/mimo-v2.6-flash", Tools: tool.NewRegistry(), AutoApprove: true, Log: log}
	var notices []string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice && strings.Contains(e.Text, "requested") {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "served by moonshot, not xiaomi") {
		t.Fatalf("notices = %q", notices)
	}
	st := a.Stats()
	if st.Mismatches != 2 || !strings.Contains(st.Mismatch, "requested xiaomi/mimo-v2.6-flash") {
		t.Fatalf("stats: mismatches=%d mismatch=%q", st.Mismatches, st.Mismatch)
	}
	n := 0
	for _, e := range log.Events() {
		if e.Type == session.TypeAssistant {
			if e.Served == nil || e.Mismatch == "" {
				t.Fatalf("assistant event without provenance: %+v", e)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("assistant events = %d", n)
	}
}

// An honest route is recorded once, not on every assistant event, and
// says nothing.
func TestHonestRouteIsQuietAndLoggedOnce(t *testing.T) {
	honest := llm.Provenance{Model: "XiaomiMiMo/MiMo-V2.6-Flash-RL", Provider: "gpu_octo", IDPrefix: "chatcmpl-", ToolIDs: "chatcmpl-tool-"}
	r1 := toolCall("chatcmpl-tool-bebdb548a1cfdcf0", "noop", `{}`)
	r1.Served = honest
	r2 := text("done")
	r2.Served = honest
	f := &fake{responses: []*llm.Response{r1, r2}}
	log := &session.Memory{}
	a := &Agent{Provider: f, Model: "XiaomiMiMo/MiMo-V2.6-Flash-RL", Tools: tool.NewRegistry(), AutoApprove: true, Log: log}
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice && strings.Contains(e.Text, "requested") {
			t.Fatalf("notice on an honest route: %s", e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if a.Stats().Mismatches != 0 {
		t.Fatal("counted a mismatch on an honest route")
	}
	withServed := 0
	for _, e := range log.Events() {
		if e.Type == session.TypeAssistant && e.Served != nil {
			withServed++
		}
	}
	if withServed != 1 {
		t.Fatalf("provenance written on %d assistant events, want 1", withServed)
	}
}
