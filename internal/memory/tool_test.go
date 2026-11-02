package memory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestToolRememberRecallForget(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	tl := &Tool{Store: s}
	run := func(in string) (string, error) { return tl.Run(context.Background(), json.RawMessage(in)) }
	out, err := run(`{"action":"remember","name":"Use Make","description":"Run tests with make test","body":"make test sets -race.","type":"feedback"}`)
	if err != nil || !strings.HasPrefix(out, "Remembered use-make (feedback, project)") {
		t.Fatalf("%q %v", out, err)
	}
	if out, _ = run(`{"action":"remember","name":"use-make","description":"Run tests with make test","body":"make test sets -race.","type":"feedback"}`); !strings.HasPrefix(out, "Already remembered") {
		t.Fatalf("dup = %q", out)
	}
	if out, _ = run(`{"action":"remember","name":"use-make","description":"Run tests with make test","body":"make test sets -race and -count=1.","type":"feedback"}`); !strings.HasPrefix(out, "Updated use-make") {
		t.Fatalf("update = %q", out)
	}
	run(`{"action":"remember","description":"The user is called Sam","scope":"global","type":"user"}`)
	if out, _ = run(`{"action":"recall","query":"tests"}`); !strings.Contains(out, `<memory name="use-make" type="feedback" scope="project">`) || strings.Contains(out, "Sam") {
		t.Fatalf("recall = %q", out)
	}
	if out, _ = run(`{"action":"recall"}`); strings.Count(out, "<memory ") != 2 {
		t.Fatalf("recall all = %q", out)
	}
	if e, _ := s.Get("use-make"); e.Uses != 2 {
		t.Fatalf("uses = %d", e.Uses)
	}
	if out, _ = run(`{"action":"recall","query":"kubernetes"}`); !strings.HasPrefix(out, "Nothing remembered") {
		t.Fatalf("miss = %q", out)
	}
	if out, err = run(`{"action":"forget","name":"use-make"}`); err != nil || !strings.HasPrefix(out, "Forgot use-make") || len(s.Entries()) != 1 {
		t.Fatalf("forget = %q %v", out, err)
	}
	if _, err = run(`{"action":"forget","name":"nope"}`); err == nil {
		t.Fatal("forget unknown accepted")
	}
	if _, err = run(`{"action":"dance"}`); err == nil {
		t.Fatal("bad action accepted")
	}
	if _, err = (&Tool{}).Run(context.Background(), json.RawMessage(`{"action":"recall"}`)); err == nil {
		t.Fatal("nil store accepted")
	}
}

// The model's forget removes what the model wrote and refuses what the
// user saved with /remember: that is theirs to remove, with /forget.
func TestToolForgetLeavesUserEntries(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	if _, _, err := s.Put(Entry{Name: "no-mocks", Description: "Never mock the database in tests", Type: "feedback"}); err != nil {
		t.Fatal(err)
	}
	tl := &Tool{Store: s}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"action":"forget","name":"no-mocks"}`))
	if err == nil || !errors.Is(err, ErrUserEntry) || !strings.Contains(err.Error(), "/forget no-mocks") {
		t.Fatalf("forget of a /remember entry = %q, %v; want a refusal pointing at /forget", out, err)
	}
	if _, ok := s.Get("no-mocks"); !ok {
		t.Fatal("the user's entry was removed by the model")
	}
	if _, err := s.Remove("no-mocks"); err != nil {
		t.Fatalf("the user's own /forget: %v", err)
	}
}

func TestToolArchiveRestore(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	tl := &Tool{Store: s}
	run := func(in string) (string, error) { return tl.Run(context.Background(), json.RawMessage(in)) }
	if _, err := run(`{"action":"remember","name":"use-make","description":"Run tests with make test","body":"make test sets -race.","type":"feedback"}`); err != nil {
		t.Fatal(err)
	}
	if out, err := run(`{"action":"archive","name":"use-make"}`); err != nil || !strings.HasPrefix(out, "Archived use-make") {
		t.Fatalf("archive = %q %v", out, err)
	}
	// Kept on disk, out of the index, recall and the listing.
	if e, ok := s.Get("use-make"); !ok || !e.Archived {
		t.Fatalf("not kept: %+v %v", e, ok)
	}
	if strings.Contains(s.Index(), "use-make") {
		t.Fatalf("index = %q", s.Index())
	}
	if out, _ := run(`{"action":"recall","query":"tests make"}`); !strings.HasPrefix(out, "Nothing remembered") {
		t.Fatalf("recall = %q", out)
	}
	if out, _ := run(`{"action":"recall"}`); !strings.HasPrefix(out, "Nothing remembered") {
		t.Fatalf("listing = %q", out)
	}
	for _, in := range []string{`{"action":"recall","include_archived":true}`, `{"action":"recall","query":"tests make","include_archived":true}`} {
		if out, _ := run(in); !strings.Contains(out, `archived="true"`) || !strings.Contains(out, "use-make") {
			t.Fatalf("%s = %q", in, out)
		}
	}
	// The file carries the flag; a file without it is active.
	if raw, _ := os.ReadFile(s.dir("project") + "/use-make.md"); !strings.Contains(string(raw), "archived: true") {
		t.Fatalf("on disk: %s", raw)
	}
	if out, err := run(`{"action":"restore","name":"use-make"}`); err != nil || !strings.HasPrefix(out, "Restored use-make") {
		t.Fatalf("restore = %q %v", out, err)
	}
	if raw, _ := os.ReadFile(s.dir("project") + "/use-make.md"); strings.Contains(string(raw), "archived") {
		t.Fatalf("flag left on disk: %s", raw)
	}
	if out, _ := run(`{"action":"recall","query":"tests make"}`); !strings.Contains(out, "use-make") {
		t.Fatalf("restored recall = %q", out)
	}
	// forget still hard-deletes, archived or not.
	run(`{"action":"archive","name":"use-make"}`)
	if _, err := run(`{"action":"forget","name":"use-make"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("use-make"); ok {
		t.Fatal("forget kept the entry")
	}
	if _, err := run(`{"action":"archive","name":"use-make"}`); err == nil {
		t.Fatal("archiving a missing entry succeeded")
	}
}

func TestToolArchiveRefusesUserEntryAndSwitch(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	if _, _, err := s.Put(Entry{Name: "mine", Description: "the user's rule", Body: "always x", Type: "feedback", Source: "user"}); err != nil {
		t.Fatal(err)
	}
	tl := &Tool{Store: s}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"action":"archive","name":"mine"}`)); !errors.Is(err, ErrUserEntry) {
		t.Fatalf("err = %v", err)
	}
	off := &Tool{Store: s, NoArchive: true}
	if _, err := off.Run(context.Background(), json.RawMessage(`{"action":"archive","name":"mine"}`)); err == nil || !strings.Contains(err.Error(), "unknown action") {
		t.Fatalf("off: %v", err)
	}
	if strings.Contains(string(off.Schema()), "archive") || !strings.Contains(string(tl.Schema()), `"archive","restore"`) {
		t.Fatal("schema does not follow the switch")
	}
	var v map[string]any
	if json.Unmarshal(tl.Schema(), &v) != nil || json.Unmarshal(off.Schema(), &v) != nil {
		t.Fatal("schema is not JSON")
	}
}
