package session

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A session that died part-way through a step's calls kept the finished
// ones' results on the log; resuming gives those back as they were, and
// only the call that was still running is unknown.
func TestFoldKeepsFinishedResultsOfADeadStep(t *testing.T) {
	user := llm.TextMessage(llm.RoleUser, "go")
	asst := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "c1", Name: "read_file"},
		{Type: llm.BlockToolUse, ID: "c2", Name: "bash"},
		{Type: llm.BlockToolUse, ID: "c3", Name: "write_file"},
	}}
	events := []Event{
		{Seq: 1, Type: TypeUserMessage, Surface: true, Message: &user},
		{Seq: 2, Type: TypeAssistant, Surface: true, Message: &asst},
		{Seq: 3, Type: TypeToolCall, CallID: "c1", Name: "read_file"},
		{Seq: 4, Type: TypeToolResult, CallID: "c1", Name: "read_file", Output: "the file's text"},
		{Seq: 5, Type: TypeToolCall, CallID: "c2", Name: "bash"},
		// dies here: c2 running, c3 never started
	}
	msgs := FoldSurface(events)
	got := map[string]llm.Block{}
	for _, b := range msgs[len(msgs)-1].Blocks {
		got[b.ToolUseID] = b
	}
	if b := got["c1"]; b.IsError || b.Text != "the file's text" {
		t.Errorf("finished call: %+v", b)
	}
	if b := got["c2"]; !b.IsError || b.Text != OrphanRunning {
		t.Errorf("running call: %+v", b)
	}
	if b := got["c3"]; !b.IsError || !strings.Contains(b.Text, "no record that this call started") {
		t.Errorf("unstarted call: %+v", b)
	}
}

// A call the harness renamed is answered under the server's id once, not
// twice: the fold used to compare the harness's id with the result's.
func TestFoldAnswersARenamedCallOnce(t *testing.T) {
	asst := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: "dup_2", ReplyID: "dup", Name: "bash"},
	}}
	res := llm.ToolResultMessage(llm.ToolResult("dup", "ok", false))
	msgs := FoldSurface([]Event{
		{Seq: 1, Type: TypeAssistant, Surface: true, Message: &asst},
		{Seq: 2, Type: TypeToolResult, Surface: true, Message: &res},
	})
	if len(msgs) != 2 {
		t.Fatalf("an answered renamed call got a second answer: %d messages", len(msgs))
	}
}
