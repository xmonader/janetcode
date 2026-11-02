package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func riskReply(thinkChars int, path string, bytes int) llm.Message {
	bl := []llm.Block{{Type: llm.BlockThinking, Text: strings.Repeat("t", thinkChars)}}
	if path != "" {
		in := fmt.Sprintf(`{"path":%q,"content":%q}`, path, strings.Repeat("x\n", bytes/2))
		bl = append(bl, llm.Block{Type: llm.BlockToolUse, ID: "w" + path, Name: "write_file", Input: []byte(in)})
	}
	return llm.Message{Role: llm.RoleAssistant, Blocks: bl}
}

func riskUser(text string) llm.Message {
	return llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: text}}}
}

func manyRules(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "- Rule %d: the catalog returns sorted items per page.\n", i)
	}
	return b.String()
}

func TestRiskyTurn(t *testing.T) {
	cases := []struct {
		name     string
		adaptive bool
		msgs     []llm.Message
		want     bool
	}{
		{"mode off: everything is risky", false, []llm.Message{riskUser("fix it")}, true},
		{"short brief small change", true, []llm.Message{riskUser("fix it"), riskReply(100, "a.go", 400)}, false},
		{"cold core write after a plan", true, []llm.Message{riskUser("build it"), riskReply(8000, "small.go", 100), riskReply(50, "core.go", 5000)}, true},
		{"core write that thought is not cold", true, []llm.Message{riskUser("build it"), riskReply(8000, "small.go", 100), riskReply(2000, "core.go", 5000)}, false},
		{"small cold write is not core", true, []llm.Message{riskUser("build it"), riskReply(8000, "small.go", 100), riskReply(50, "core.go", 1000)}, false},
		{"long brief, large change", true, []llm.Message{riskUser(manyRules(20)), riskReply(100, "svc.go", 600)}, true},
		{"long brief, small change", true, []llm.Message{riskUser(manyRules(20)), riskReply(100, "svc.go", 100)}, false},
		{"large change, short brief", true, []llm.Message{riskUser(manyRules(5)), riskReply(100, "svc.go", 600)}, false},
	}
	for _, c := range cases {
		a := &Agent{Adaptive: c.adaptive, Messages: c.msgs}
		if got := a.riskyTurn(); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPlannedUnwritten(t *testing.T) {
	plan := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockThinking, Text: strings.Repeat("x ", 3000) + " Plan: write `store.go` and src/core.go, then store_test.go."}}}
	cases := []struct {
		name string
		msgs []llm.Message
		want int
	}{
		{"plan names two product files", []llm.Message{riskUser("go"), plan}, 2},
		{"one is written by the plan reply itself", []llm.Message{riskUser("go"), func() llm.Message {
			p := plan
			p.Blocks = append(append([]llm.Block{}, plan.Blocks...), riskReply(0, "store.go", 100).Blocks[1])
			return p
		}()}, 1},
		{"not a plan", []llm.Message{riskUser("go"), riskReply(100, "", 0)}, 0},
	}
	for _, c := range cases {
		if got := plannedUnwritten(c.msgs); len(got) != c.want {
			t.Errorf("%s: got %v want %d", c.name, got, c.want)
		}
	}
}

func TestAdaptiveGatesPlanCarry(t *testing.T) {
	resp := &llm.Response{Message: riskReply(8000, "a.go", 100)}
	a := &Agent{Adaptive: true, Messages: []llm.Message{riskUser("go"), resp.Message}}
	if a.planCarryNote(resp) != "" {
		t.Fatal("adaptive: no named unwritten files, so no note")
	}
	a.Adaptive = false
	if a.planCarryNote(resp) == "" {
		t.Fatal("default: all on")
	}
}
