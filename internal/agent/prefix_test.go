package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func msg(role llm.Role, text string) llm.Message {
	return llm.Message{Role: role, Blocks: []llm.Block{{Type: llm.BlockText, Text: text}}}
}

// TestPrefixWatch: a request that only appends keeps the cached prefix; one
// that edits a message already sent throws away everything from there on,
// and the report has to name which message.
func TestPrefixWatch(t *testing.T) {
	var w prefixWatch
	sys, tools := "you are a coding agent", []llm.ToolDef{{Name: "bash", Description: "run", InputSchema: []byte(`{}`)}}
	one := []llm.Message{msg(llm.RoleUser, "first")}
	two := append(append([]llm.Message{}, one...), msg(llm.RoleAssistant, "answer"))

	w.note(1, sys, one, tools)
	w.note(1, sys, two, tools)
	if len(w.drifts) != 0 {
		t.Fatalf("appending must not count as drift: %+v", w.drifts)
	}

	// Editing a message already sent.
	edited := append([]llm.Message{}, two...)
	edited[0] = msg(llm.RoleUser, "first, revised")
	w.note(2, sys, edited, tools)
	if len(w.drifts) != 1 || w.drifts[0].At != 0 {
		t.Fatalf("an edited message should be located: %+v", w.drifts)
	}
	if !strings.Contains(w.drifts[0].NameAt, "user") {
		t.Fatalf("the message should be described: %q", w.drifts[0].NameAt)
	}

	// A changing system prompt is the classic silent cache killer.
	w.note(3, sys+" (today is Tuesday)", edited, tools)
	last := w.drifts[len(w.drifts)-1]
	if !last.System {
		t.Fatalf("a changed system prompt must be reported: %+v", last)
	}

	// So is a tool list that grows mid-session.
	more := append(append([]llm.ToolDef{}, tools...), llm.ToolDef{Name: "grep", InputSchema: []byte(`{}`)})
	w.note(4, sys+" (today is Tuesday)", edited, more)
	if last := w.drifts[len(w.drifts)-1]; !last.Tools {
		t.Fatalf("a changed tool list must be reported: %+v", last)
	}

	// And a compaction, which shortens the history without editing what
	// survives, is still a rewrite.
	w.note(5, sys+" (today is Tuesday)", edited[:1], more)
	if last := w.drifts[len(w.drifts)-1]; last.At != 1 {
		t.Fatalf("a shortened history should be located: %+v", last)
	}
}

func TestPrefixReport(t *testing.T) {
	a := &Agent{}
	if got := a.PrefixReport(); !strings.Contains(got, "no request") {
		t.Fatalf("got %q", got)
	}
	sys := "s"
	a.prefix.note(1, sys, []llm.Message{msg(llm.RoleUser, "a")}, nil)
	a.prefix.note(1, sys, []llm.Message{msg(llm.RoleUser, "a"), msg(llm.RoleAssistant, "b")}, nil)
	if got := a.PrefixReport(); !strings.Contains(got, "only appended") {
		t.Fatalf("a clean session should say so: %q", got)
	}
	a.prefix.note(2, sys, []llm.Message{msg(llm.RoleUser, "changed")}, nil)
	got := a.PrefixReport()
	if !strings.Contains(got, "stopped matching") || !strings.Contains(got, "billed as fresh") {
		t.Fatalf("got %q", got)
	}
}
