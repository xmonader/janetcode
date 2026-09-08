package memory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestKeyPrefersMarkerThenRemoteThenPath(t *testing.T) {
	root := t.TempDir()
	k1 := Key(root)
	if !strings.HasPrefix(k1, filepath.Base(root)+"-") {
		t.Fatalf("path key = %q", k1)
	}
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	exec.Command("git", "-C", root, "remote", "add", "origin", "git@github.com:Xmonader/JanetCode.git").Run()
	if k := Key(root); k != "github.com-xmonader-janetcode" {
		t.Fatalf("remote key = %q", k)
	}
	other := t.TempDir()
	exec.Command("git", "-C", other, "init", "-q").Run()
	exec.Command("git", "-C", other, "remote", "add", "origin", "https://user@github.com/xmonader/janetcode.git/").Run()
	if Key(other) != Key(root) {
		t.Fatalf("clones differ: %q vs %q", Key(other), Key(root))
	}
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	os.WriteFile(filepath.Join(root, MarkerFile), []byte("my project/v2\n"), 0o644)
	if k := Key(root); k != "my-project-v2" {
		t.Fatalf("marker key = %q", k)
	}
}

func TestStoreAddRemoveSection(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	if s.Section() != "" || len(s.Entries()) != 0 {
		t.Fatal("empty store not empty")
	}
	if ok, err := s.Add("  - Use   make test,\nnever go test directly "); err != nil || !ok {
		t.Fatalf("add: %v %v", ok, err)
	}
	if ok, _ := s.Add("use make test, never go test directly"); ok {
		t.Fatal("duplicate added")
	}
	if _, err := s.Add("   "); err == nil {
		t.Fatal("empty accepted")
	}
	s.Add("Deploys go through doas, not sudo")
	if e := s.Entries(); len(e) != 2 || e[0] != "Use make test, never go test directly" {
		t.Fatalf("entries = %q", e)
	}
	if sec := s.Section(); !strings.Contains(sec, "# Project memory") || !strings.Contains(sec, "- Deploys go through doas") {
		t.Fatalf("section = %q", sec)
	}
	if gone, err := s.Remove(1); err != nil || gone != "Use make test, never go test directly" {
		t.Fatalf("remove: %q %v", gone, err)
	}
	if _, err := s.Remove(5); err == nil {
		t.Fatal("bad index accepted")
	}
	if e := Open(filepath.Dir(filepath.Dir(s.Path)), "").Entries(); len(e) != 0 {
		t.Fatal("different project sees entries")
	}
	again := &Store{Path: s.Path, Key: s.Key}
	if e := again.Entries(); len(e) != 1 || e[0] != "Deploys go through doas, not sudo" {
		t.Fatalf("reopen = %q", e)
	}
	for i := 0; i < MaxEntries+5; i++ {
		s.Add(strings.Repeat("x", 3) + string(rune('a'+i%26)) + strings.Repeat("y", i%7) + " entry " + strings.Repeat("z", i))
	}
	if n := len(s.Entries()); n != MaxEntries {
		t.Fatalf("entries after overflow = %d", n)
	}
}

type fakeProvider struct {
	reply string
	req   llm.Request
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Stream(_ context.Context, req llm.Request, _ func(llm.Event)) (*llm.Response, error) {
	f.req = req
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, f.reply), StopReason: llm.StopEndTurn}, nil
}

func TestExtractParsesAndTrims(t *testing.T) {
	f := &fakeProvider{reply: "- The user wants commits under 50 lines.\n2. Tests run with make test.\nNONE\n\n"}
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "keep commits small"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "secret"}, {Type: llm.BlockText, Text: "ok"}}},
		llm.ToolResultMessage(llm.ToolResult("t", strings.Repeat("o", 5000), false)),
	}
	got, err := Extract(context.Background(), f, "m", msgs, []string{"old fact"})
	if err != nil || len(got) != 2 || got[0] != "The user wants commits under 50 lines." || got[1] != "Tests run with make test." {
		t.Fatalf("got %q err %v", got, err)
	}
	if f.req.System != extractSystem || f.req.MaxTokens != 400 {
		t.Fatalf("request = %+v", f.req)
	}
	last := f.req.Messages[len(f.req.Messages)-1].Text()
	if !strings.Contains(last, "- old fact") {
		t.Fatalf("existing memory missing: %q", last)
	}
	for _, m := range f.req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockThinking || len(b.Text) > 1000 {
				t.Fatalf("thinking or untrimmed result sent: %+v", b.Type)
			}
		}
	}
	f.reply = "NONE"
	if got, _ := Extract(context.Background(), f, "m", msgs, nil); len(got) != 0 {
		t.Fatalf("NONE parsed as %q", got)
	}
}
