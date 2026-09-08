package memory

import (
	"context"
	"encoding/json"
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
