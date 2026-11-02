package agent

import (
	"github.com/xmonader/janetcode/internal/llm"
	"strings"
	"testing"
)

const logstoreBrief = `# Log store

Replay the file in this order, and decide at the first problem found:

1. Fewer than 7 bytes remain: that is a torn tail and is truncated.
2. The header is invalid, which is Corrupt even if the record is short.

Failures print ` + "`error: MESSAGE`" + ` and exit with code 2.

The cursor is strictly after the last returned key for pagination.

## Tests

Fewer than 3 bytes remain is not a rule here at all.
`

func kinds(rs []edgeRule) []edgeKind {
	var k []edgeKind
	for _, r := range rs {
		k = append(k, r.kind)
	}
	return k
}

func TestEdgeRulesKinds(t *testing.T) {
	rs := edgeRules(logstoreBrief)
	got := map[edgeKind]bool{}
	for _, r := range rs {
		got[r.kind] = true
		if strings.Contains(r.text, "not a rule here") {
			t.Errorf("a Tests section sentence became a rule: %q", r.text)
		}
	}
	for _, k := range []edgeKind{edgeThreshold, edgeEvenIf, edgeMessage, edgeStrict} {
		if !got[k] {
			t.Errorf("kind %d not found in %v", k, kinds(rs))
		}
	}
	for _, r := range rs {
		if r.kind == edgeMessage && r.literal != "error:" {
			t.Errorf("literal = %q", r.literal)
		}
	}
}

func TestUncheckedEdgeRules(t *testing.T) {
	cases := []struct {
		name   string
		checks []string
		want   int // rules left unchecked, -1 = just compare against none
		gone   string
	}{
		{"no checks", nil, -1, ""},
		{"torn tail test names the rule but not its edge", []string{"func TestTornTail(t *testing.T) {\n\tassert.Equal(t, truncated, tornTail)\n}"}, -1, ""},
		{"short header asserted", []string{"func TestHeader(t *testing.T) {\n\tassert.Equal(t, Corrupt, replay(shortInvalidHeader))\n}"}, -1, "even if"},
	}
	all := len(uncheckedEdgeRules(logstoreBrief, nil))
	if all < 3 {
		t.Fatalf("only %d unchecked with no checks", all)
	}
	for _, c := range cases {
		un := uncheckedEdgeRules(logstoreBrief, c.checks)
		if c.gone != "" {
			for _, r := range un {
				if strings.Contains(strings.ToLower(r.text), c.gone) {
					t.Errorf("%s: rule still unchecked: %q", c.name, r.text)
				}
			}
			if len(un) >= all {
				t.Errorf("%s: nothing was reached", c.name)
			}
		}
	}
}

func TestEdgeRulesNoteCapsAndSpans(t *testing.T) {
	note := edgeRulesNote("the brief", logstoreBrief, nil)
	if n := strings.Count(note, "\n1. ") + strings.Count(note, "\n2. ") + strings.Count(note, "\n3. ") + strings.Count(note, "\n4. "); n != 3 {
		t.Fatalf("want 3 listed rules, got %d:\n%s", n, note)
	}
	if edgeRulesNote("the brief", "Just make the button blue.", nil) != "" {
		t.Fatal("a brief without edge rules must give no note")
	}
}

func TestPickSpansKinds(t *testing.T) {
	rs := []edgeRule{{kind: edgeThreshold, text: "a"}, {kind: edgeThreshold, text: "b"}, {kind: edgeOrder, text: "c"}, {kind: edgeMessage, text: "d"}}
	got := pickEdgeRules(rs, 3)
	if len(got) != 3 || got[0].text != "a" || got[1].text != "c" || got[2].text != "d" {
		t.Fatalf("got %+v", got)
	}
}

func TestTurnSpecAndChecks(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "old request"}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: logstoreBrief}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "b1", Name: "bash", Input: []byte(`{"command":"go test ./..."}`)},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: []byte(`{"path":"x_test.go","content":"assert short"}`)},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "[harness] note", Synthetic: true}}},
	}
	spec, checks := turnSpecAndChecks(msgs)
	if !strings.Contains(spec, "torn tail") || strings.Contains(spec, "old request") {
		t.Fatalf("spec = %q", spec)
	}
	if len(checks) != 2 || checks[0] != "go test ./..." || checks[1] != "assert short" {
		t.Fatalf("checks = %q", checks)
	}
}
