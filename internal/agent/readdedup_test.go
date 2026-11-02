package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func rdAgent(t *testing.T) *Agent {
	a, _ := newAgent(t, &fake{}, nil)
	return a
}

func rdUse(id, path string) llm.Block {
	return llm.Block{Type: llm.BlockToolUse, ID: id, Name: "read_file", Input: []byte(`{"path":"` + path + `"}`)}
}

// history is what the model has seen: the reads and their results.
func rdHistory(pairs ...[2]string) []llm.Message {
	var msgs []llm.Message
	for _, p := range pairs {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{llm.ToolResult(p[0], p[1], false)}})
	}
	return msgs
}

func TestDedupReadsRules(t *testing.T) {
	body := strings.Repeat("line\n", 50)
	edit := llm.Block{Type: llm.BlockToolUse, ID: "e", Name: "write_file", Input: []byte(`{"path":"a.txt","content":"x"}`)}
	type step struct {
		uses    []llm.Block
		texts   []string
		hist    []llm.Message // nil: history holds every earlier result
		pointer bool          // the last result should become a pointer
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"unchanged live re-read is a pointer", []step{
			{uses: []llm.Block{rdUse("c1", "a.txt")}, texts: []string{body}},
			{uses: []llm.Block{rdUse("c2", "a.txt")}, texts: []string{body}, pointer: true},
		}},
		{"changed text is returned", []step{
			{uses: []llm.Block{rdUse("c1", "a.txt")}, texts: []string{body}},
			{uses: []llm.Block{rdUse("c2", "a.txt")}, texts: []string{body + "more\n"}},
		}},
		{"edit in between forgets the view", []step{
			{uses: []llm.Block{rdUse("c1", "a.txt")}, texts: []string{body}},
			{uses: []llm.Block{edit}, texts: []string{"ok"}},
			{uses: []llm.Block{rdUse("c2", "a.txt")}, texts: []string{body}},
		}},
		{"edit of another file keeps it", []step{
			{uses: []llm.Block{rdUse("c1", "b.txt")}, texts: []string{body}},
			{uses: []llm.Block{edit}, texts: []string{"ok"}},
			{uses: []llm.Block{rdUse("c2", "b.txt")}, texts: []string{body}, pointer: true},
		}},
		{"earlier copy compacted away", []step{
			{uses: []llm.Block{rdUse("c1", "a.txt")}, texts: []string{body}},
			{uses: []llm.Block{rdUse("c2", "a.txt")}, texts: []string{body}, hist: rdHistory([2]string{"c1", "[stubbed]"})},
		}},
		{"a second re-read right after a pointer gets the text", []step{
			{uses: []llm.Block{rdUse("c1", "a.txt")}, texts: []string{body}},
			{uses: []llm.Block{rdUse("c2", "a.txt")}, texts: []string{body}, pointer: true},
			{uses: []llm.Block{rdUse("c3", "a.txt")}, texts: []string{body}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := rdAgent(t)
			var hist []llm.Message
			for i, s := range c.steps {
				var res []llm.Block
				for j, u := range s.uses {
					res = append(res, llm.ToolResult(u.ID, s.texts[j], false))
				}
				h := hist
				if s.hist != nil {
					h = s.hist
				}
				a.dedupReads(h, s.uses, res)
				got := strings.HasPrefix(res[len(res)-1].Text, repeatPrefix)
				if got != s.pointer {
					t.Fatalf("step %d: pointer=%v want %v (%q)", i, got, s.pointer, res[len(res)-1].Text)
				}
				hist = append(hist, rdHistory([2]string{s.uses[0].ID, res[0].Text})...)
			}
		})
	}
	a := rdAgent(t)
	a.Off = map[string]bool{"readdedup": true}
	u := rdUse("c1", "a.txt")
	r := []llm.Block{llm.ToolResult("c1", body, false)}
	a.dedupReads(nil, []llm.Block{u}, r)
	r2 := []llm.Block{llm.ToolResult("c2", body, false)}
	if a.dedupReads(rdHistory([2]string{"c1", body}), []llm.Block{rdUse("c2", "a.txt")}, r2) != 0 {
		t.Fatal("off switch ignored")
	}
	var _ tool.Tool = tool.ReadFile{}
}

func TestFoldStandIns(t *testing.T) {
	for _, n := range []int{3, 4, 5, 12} {
		var uses, res []llm.Block
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("c%d", i)
			uses = append(uses, rdUse(id, "a.txt"))
			res = append(res, llm.ToolResult(id, dedupedResult, false))
		}
		dropped, note := foldStandIns(uses, res, standInsKept)
		res[n-1].Text += "\n\n[note]"
		want := max(n-standInsKept, 0)
		if len(dropped) != want {
			t.Fatalf("n=%d dropped %d want %d", n, len(dropped), want)
		}
		out := withoutDropped(res, dropped, note, "\n\n[note]")
		if len(out) != n-want {
			t.Fatalf("n=%d kept %d", n, len(out))
		}
		if want > 0 {
			last := out[len(out)-1].Text
			if !strings.Contains(last, "dropped from the history") || !strings.Contains(last, "[note]") {
				t.Fatalf("n=%d last = %q", n, last)
			}
		}
	}
}
