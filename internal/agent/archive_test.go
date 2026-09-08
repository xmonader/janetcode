package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestArchivePutGetAndResume(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "arch")
	ar := NewArchive(dir)
	id, err := ar.Put("use-1", "bash", "line one\nline two\n")
	if err != nil || id != "ctx-1" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if again, _ := ar.Put("use-1", "bash", "ignored"); again != id {
		t.Fatalf("same tool use archived twice: %s", again)
	}
	if id2, _ := ar.Put("use-2", "grep", "x"); id2 != "ctx-2" {
		t.Fatalf("second id = %s", id2)
	}
	// A fresh Archive over the same directory continues the sequence and
	// remembers which tool use produced what.
	ar2 := NewArchive(dir)
	if got := ar2.IDFor("use-1"); got != "ctx-1" {
		t.Fatalf("IDFor after reopen = %q", got)
	}
	if id3, _ := ar2.Put("use-3", "bash", "y"); id3 != "ctx-3" {
		t.Fatalf("third id = %s", id3)
	}
	e, body, err := ar2.Get("ctx-1")
	if err != nil || e.Name != "bash" || e.Size != 18 || body != "line one\nline two\n" {
		t.Fatalf("get: %+v %q %v", e, body, err)
	}
	if _, _, err := ar2.Get("ctx-99"); !errors.Is(err, ErrArchiveMissing) {
		t.Fatalf("missing id err = %v", err)
	}
	if _, _, err := ar2.Get("../etc/passwd"); !errors.Is(err, ErrArchiveMissing) {
		t.Fatalf("bad id err = %v", err)
	}
	if l := ar2.List(); len(l) != 3 || l[0].ID != "ctx-3" {
		t.Fatalf("list = %+v", l)
	}
}

func TestArchiveEvictsOldest(t *testing.T) {
	ar := NewArchive(t.TempDir())
	big := strings.Repeat("a", ArchiveCap/2+1)
	ar.Put("u1", "bash", big)
	ar.Put("u2", "bash", big)
	ar.Put("u3", "bash", "small")
	if _, _, err := ar.Get("ctx-1"); !errors.Is(err, ErrArchiveMissing) {
		t.Fatalf("oldest should be evicted, err = %v", err)
	}
	if _, _, err := ar.Get("ctx-3"); err != nil {
		t.Fatalf("newest lost: %v", err)
	}
	if ar.IDFor("u1") != "" {
		t.Fatal("evicted entry still mapped")
	}
}

type bigTool struct{ out string }

func (bigTool) Name() string                                           { return "bigtool" }
func (bigTool) Description() string                                    { return "" }
func (bigTool) Mutating() bool                                         { return false }
func (bigTool) Schema() json.RawMessage                                { return json.RawMessage(`{"type":"object"}`) }
func (b bigTool) Run(context.Context, json.RawMessage) (string, error) { return b.out, nil }

func TestLongOutputIsArchivedAndExpandable(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 3000; i++ {
		fmt.Fprintf(&sb, "row %04d %s\n", i, strings.Repeat("x", 20))
	}
	full := sb.String()
	ar := NewArchive(t.TempDir())
	reg := tool.NewRegistry(bigTool{out: full}, &ExpandOutput{Archive: ar})
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bigtool", `{}`),
		toolCall("t2", "expand_output", `{"id":"ctx-1","grep":"row 1234"}`),
		toolCall("t3", "expand_output", `{"id":"ctx-1","offset":2998,"limit":10}`),
		text("done"),
	}}
	a := &Agent{Provider: f, Tools: reg, Model: "m", Archive: ar, CompactAt: -1}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := func(i int) string { return a.Messages[i].Blocks[0].Text }
	if got := res(2); len(got) > InlineOutput+400 || !strings.HasPrefix(got, "[output is ") || !strings.Contains(got, "saved as ctx-1") || !strings.Contains(got, "row 0001") || !strings.Contains(got, "row 3000") {
		t.Fatalf("preview wrong (len %d): %.300s", len(got), got)
	}
	if got := res(4); !strings.Contains(got, "1234\trow 1234") || strings.Contains(got, "row 1235") {
		t.Fatalf("grep result = %q", got)
	}
	if got := res(6); !strings.Contains(got, "lines 2998-3000") || !strings.Contains(got, "3000\trow 3000") {
		t.Fatalf("slice result = %q", got)
	}
	// Nothing about expand_output's own result was archived.
	if len(ar.List()) != 1 {
		t.Fatalf("archive has %d entries", len(ar.List()))
	}
	// The user-visible event carries the same preview the model saw.
	x := &ExpandOutput{Archive: ar}
	out, err := x.Run(context.Background(), nil)
	if err != nil || !strings.Contains(out, "ctx-1\tbigtool") {
		t.Fatalf("list = %q %v", out, err)
	}
	if out, err = x.Run(context.Background(), json.RawMessage(`{"grep":"row 2999"}`)); err != nil || !strings.Contains(out, "ctx-1 (bigtool):\n2999\trow 2999") {
		t.Fatalf("grep all = %q %v", out, err)
	}
	if _, err = x.Run(context.Background(), json.RawMessage(`{"id":"ctx-7"}`)); !errors.Is(err, ErrArchiveMissing) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestStashWithoutArchiveTruncates(t *testing.T) {
	a := &Agent{}
	out := a.stash("t", "bash", strings.Repeat("q", InlineOutput*2))
	if len(out) > InlineOutput+100 || !strings.Contains(out, "omitted") || strings.Contains(out, "ctx-") {
		t.Fatalf("len=%d", len(out))
	}
}

func TestPruneArchivesBeforeTrimming(t *testing.T) {
	ar := NewArchive(t.TempDir())
	long := strings.Repeat("y", 10_000)
	a := &Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Archive: ar, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "1", Name: "bash", Input: json.RawMessage(`{}`)}}},
		llm.ToolResultMessage(llm.ToolResult("1", "output "+long, false)),
		llm.TextMessage(llm.RoleAssistant, "a"), llm.TextMessage(llm.RoleUser, "b"),
		llm.TextMessage(llm.RoleAssistant, "c"), llm.TextMessage(llm.RoleUser, "d"),
		llm.TextMessage(llm.RoleAssistant, "e"), llm.TextMessage(llm.RoleUser, "f"),
	}}
	a.Prune()
	got := a.Messages[2].Blocks[0].Text
	if !strings.HasPrefix(got, "[output is 9.8 KB") || !strings.Contains(got, "saved as ctx-1") || len(got) > pruneKeep+400 {
		t.Fatalf("pruned = %.200q (len %d)", got, len(got))
	}
	if _, body, err := ar.Get("ctx-1"); err != nil || body != "output "+long {
		t.Fatalf("archive body wrong: %v", err)
	}
	// Pruning again keeps the single note and does not re-archive.
	a.Prune()
	if n := strings.Count(a.Messages[2].Blocks[0].Text, "[output is"); n != 1 || len(ar.List()) != 1 {
		t.Fatalf("notes=%d entries=%d", n, len(ar.List()))
	}
	if _, err := os.Stat(filepath.Join(ar.Dir, "ctx-1.txt")); err != nil {
		t.Fatal(err)
	}
}
