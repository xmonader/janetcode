package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func bigHead(n int) []llm.Message {
	return []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("a", n)),
		llm.TextMessage(llm.RoleAssistant, "ok"),
	}
}

func TestSummaryWouldShrink(t *testing.T) {
	cases := []struct {
		name       string
		before     int
		head       []llm.Message
		rich, want bool
	}{
		{"small context is never judged", 3000, bigHead(100), false, true},
		{"large head shrinks", 20_000, bigHead(60_000), false, true},
		{"head is a sliver of a big context", 100_000, bigHead(4_000), false, false},
		{"just under 5 percent", 40_000, bigHead(8_000 + 4*(2048+200)/4), false, false},
	}
	for _, c := range cases {
		a := &Agent{}
		if got := a.summaryWouldShrink(c.before, c.head, c.rich); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCompactRequestNote(t *testing.T) {
	long := strings.Repeat("rule ", 200)
	tail := []llm.Message{llm.TextMessage(llm.RoleAssistant, "working")}
	pend := llm.TextMessage(llm.RoleUser, long)
	cases := []struct {
		name    string
		req     string
		tail    []llm.Message
		pending *llm.Message
		off     string
		want    bool
	}{
		{"long request missing from the tail", long, tail, nil, "", true},
		{"short request is in the record already", "fix it", tail, nil, "", false},
		{"pending message holds it", long, tail, &pend, "", false},
		{"tail holds it", long, []llm.Message{llm.TextMessage(llm.RoleUser, long)}, nil, "", false},
		{"switched off", long, tail, nil, "compactskip", false},
		{"no request", "", tail, nil, "", false},
	}
	for _, c := range cases {
		a := &Agent{Off: map[string]bool{}}
		if c.off != "" {
			a.Off[c.off] = true
		}
		a.integ.userText = c.req
		got := a.compactRequestNote(c.tail, c.pending)
		if (got != "") != c.want || (c.want && !strings.Contains(got, strings.TrimSpace(long))) {
			t.Errorf("%s: note %q", c.name, got[:min(len(got), 40)])
		}
	}
}
