package memory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestKeyPrefersMarkerThenRemoteThenPath(t *testing.T) {
	root := t.TempDir()
	if k := Key(root); !strings.HasPrefix(k, filepath.Base(root)+"-") {
		t.Fatalf("path key = %q", k)
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

func TestPutDedupesUpdatesAndScopes(t *testing.T) {
	data := t.TempDir()
	s := Open(data, t.TempDir())
	if s.Index() != "" || len(s.Entries()) != 0 {
		t.Fatal("empty store not empty")
	}
	e, st, err := s.Put(Entry{Description: "Never run go test directly, always use make test", Body: "Use `make test`.\n\n**Why:** it sets the race flag."})
	if err != nil || st != Created || e.Type != TypeFeedback || e.Scope != "project" || e.Name != "go-test-directly-test" && !strings.Contains(e.Name, "test") {
		t.Fatalf("put = %+v %v %v", e, st, err)
	}
	if _, st, _ = s.Put(Entry{Description: "never run go test directly, always use make test", Body: "Use `make test`.\n\n**Why:** it sets the race flag."}); st != Unchanged {
		t.Fatalf("duplicate status = %v", st)
	}
	// Nearly the same description merges into the existing entry.
	e2, st, _ := s.Put(Entry{Description: "Always use make test, never run go test directly", Body: "make test, with race"})
	if st != Updated || e2.Name != e.Name || e2.Body != "make test, with race" {
		t.Fatalf("merge = %+v %v", e2, st)
	}
	if _, _, err := s.Put(Entry{}); err == nil {
		t.Fatal("empty accepted")
	}
	g, st, _ := s.Put(Entry{Name: "Likes Terse Replies", Description: "The user prefers replies under ten lines", Scope: "global", Type: TypeUser})
	if st != Created || g.Name != "likes-terse-replies" || g.Scope != "global" || !strings.Contains(g.Path, GlobalKey) {
		t.Fatalf("global = %+v", g)
	}
	s.Put(Entry{Description: "Deploys go through doas, see https://wiki.example/deploy", Type: "bogus"})
	all := s.Entries()
	if len(all) != 3 || all[0].Type != TypeReference {
		t.Fatalf("entries = %+v", all)
	}
	if got, ok := s.Get("likes terse replies"); !ok || got.Description != "The user prefers replies under ten lines" {
		t.Fatalf("get = %+v %v", got, ok)
	}
	idx := s.Index()
	for _, want := range []string{"# Memory", "- likes-terse-replies (user, global): The user prefers", "(feedback): Always use make test"} {
		if !strings.Contains(idx, want) {
			t.Fatalf("index missing %q:\n%s", want, idx)
		}
	}
	// Another project sees only the global entry; a reopened store sees all.
	if e := Open(data, t.TempDir()).Entries(); len(e) != 1 || e[0].Scope != "global" {
		t.Fatalf("other project sees %+v", e)
	}
	if e := Open(data, filepath.Dir(s.dir("project"))).Entries(); len(e) < 1 {
		t.Fatal("reopen lost entries")
	}
	if gone, err := s.Remove("2"); err != nil || gone.Name == "" {
		t.Fatalf("remove by number: %+v %v", gone, err)
	}
	if _, err := s.Remove("nope"); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := s.Remove(g.Name); err != nil || len(s.Entries()) != 1 {
		t.Fatalf("remove by name: %v %d", err, len(s.Entries()))
	}
}

func TestRecallTouchAndTrim(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "migrations", Description: "Database migrations use goose", Body: "Run goose up from the db directory; never edit an applied migration."})
	s.Put(Entry{Name: "deploy", Description: "Deploys go through the ansible playbook", Body: "ansible-playbook site.yml"})
	s.Put(Entry{Name: "terse", Description: "User prefers terse replies", Scope: "global"})
	got := s.Recall("add a new database migration for the users table", 4)
	if len(got) != 1 || got[0].Name != "migrations" {
		t.Fatalf("recall = %+v", got)
	}
	if got := s.Recall("fix the typo in main.go", 4); len(got) != 0 {
		t.Fatalf("weak signal recalled %+v", got)
	}
	s.Touch(got...)
	s.Touch(s.Recall("goose migration", 1)...)
	if e, _ := s.Get("migrations"); e.Uses != 2 {
		t.Fatalf("uses = %d", e.Uses)
	}
	if r := Render(s.Entries()[0]); !strings.HasPrefix(r, "<memory name=") || !strings.HasSuffix(r, "</memory>") {
		t.Fatalf("render = %q", r)
	}
	for i := 0; i < MaxEntries+3; i++ {
		s.Put(Entry{Name: "filler-" + itoa(i), Description: "Filler " + itoa(i), Body: "b"})
	}
	n := 0
	for _, e := range s.Entries() {
		if e.Scope == "project" {
			n++
		}
	}
	if n != MaxEntries {
		t.Fatalf("project entries after overflow = %d", n)
	}
	if _, ok := s.Get("migrations"); !ok {
		t.Fatal("used entry was trimmed before unused fillers")
	}
}

func itoa(i int) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(i)) }

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestMigrateFlatFile(t *testing.T) {
	data, cwd := t.TempDir(), t.TempDir()
	os.MkdirAll(filepath.Join(data, "memory"), 0o755)
	old := filepath.Join(data, "memory", Key(cwd)+".md")
	os.WriteFile(old, []byte("# janetcode memory\n- Tests run with make test.\n- Deploys use doas, never sudo.\n"), 0o644)
	s := Open(data, cwd)
	es := s.Entries()
	if len(es) != 2 || es[0].Source != "auto" {
		t.Fatalf("migrated = %+v", es)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("flat file still there")
	}
	if len(Open(data, cwd).Entries()) != 2 {
		t.Fatal("second open changed entries")
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

func TestExtractParsesAppliesAndReplaces(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	s.Put(Entry{Name: "commit-size", Description: "Commits under 100 lines", Body: "old"})
	f := &fakeProvider{reply: "```\n" +
		`{"type":"feedback","scope":"project","name":"commit-size","description":"Commits must stay under 50 lines","body":"The user asked for commits under 50 lines.","replaces":"commit-size"}` + "\n" +
		`- {"type":"user","scope":"global","name":"terse","description":"Prefers terse replies","body":"Keep replies short."}` + "\n" +
		"Tests run with make test, never plain go test.\nNONE\n```"}
	msgs := []llm.Message{
		llm.TextMessage(llm.RoleUser, "keep commits small"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "secret"}, {Type: llm.BlockText, Text: "ok"}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "more"}, {Type: llm.BlockText, Text: "<skill name=\"x\">attached</skill>"}}},
		llm.ToolResultMessage(llm.ToolResult("t", strings.Repeat("o", 5000), false)),
	}
	xs, err := Extract(context.Background(), f, "m", msgs, s.Entries())
	if err != nil || len(xs) != 3 || xs[0].Replaces != "commit-size" || xs[1].Scope != "global" || !strings.HasPrefix(xs[2].Description, "Tests run with make test") {
		t.Fatalf("xs=%+v err=%v", xs, err)
	}
	if f.req.System != extractSystem || f.req.MaxTokens != 800 || !strings.Contains(f.req.Messages[len(f.req.Messages)-1].Text(), "- commit-size (project, project): Commits under 100 lines") {
		t.Fatalf("request = %+v", f.req)
	}
	for _, m := range f.req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockThinking || len(b.Text) > 1000 || strings.HasPrefix(b.Text, "<skill") {
				t.Fatalf("thinking, attachment or untrimmed result sent: %q", b.Text[:40])
			}
		}
	}
	changed, err := s.Apply(xs)
	if err != nil || len(changed) != 3 {
		t.Fatalf("apply: %+v %v", changed, err)
	}
	if e, _ := s.Get("commit-size"); e.Description != "Commits must stay under 50 lines" || e.Body != "The user asked for commits under 50 lines." {
		t.Fatalf("replace failed: %+v", e)
	}
	if e, _ := s.Get("terse"); e.Scope != "global" || e.Type != TypeUser || e.Source != "auto" {
		t.Fatalf("global entry = %+v", e)
	}
	if changed, _ := s.Apply(xs); len(changed) != 0 {
		t.Fatalf("re-apply changed %+v", changed)
	}
	if got := ParseExtracted("NONE"); len(got) != 0 {
		t.Fatalf("NONE parsed as %+v", got)
	}
	if !LooksLikeFeedback("please don't touch the Makefile") || LooksLikeFeedback("what does main do") {
		t.Fatal("feedback heuristic wrong")
	}
	_ = time.Now
}
