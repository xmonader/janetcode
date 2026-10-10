package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPurityScreenCountsSavedPrompts(t *testing.T) {
	calls := func(repo string, cmds ...string) log {
		var l log
		for _, c := range cmds {
			l.calls = append(l.calls, call{repo: repo, command: c})
		}
		return l
	}
	r := screen([]log{
		calls("/a", "cat x", "cat x", "go test ./...", "go test ./..."),
		calls("/a", "cat x", "cat x", "go test ./...", "go test ./..."),
		calls("/b", "cat x"), // another repository starts over
		calls("/a", "./run.sh", "./run.sh", "./run.sh", "./run.sh"),
	})
	// cat x: runs 4 (session 2, call 2) is the only one after three
	// clean runs in /a. go test and ./run.sh never count.
	if r.calls != 13 || r.saved != 1 || r.withBash != 4 {
		t.Fatalf("result %+v", r)
	}
	if r.upper != 1+1+1 { // the 4th cat x, the 4th go test, the 4th ./run.sh
		t.Fatalf("upper bound %d", r.upper)
	}
}

func TestPurityScreenReadsLogs(t *testing.T) {
	dir := t.TempDir()
	sess := filepath.Join(dir, "task-s1", "janetcode-sessions")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatal(err)
	}
	data := `{"type":"meta","meta":{"id":"x","cwd":"/w","created":"2026-10-10T01:00:00Z"}}
{"type":"event","seq":1,"t":"tool/call","name":"bash","arguments":"{\"command\":\"ls\"}"}
{"type":"event","seq":2,"t":"tool/call","name":"bash","arguments":"{\"command\":\"npm run dev\",\"background\":true}"}
{"type":"event","seq":3,"t":"tool/call","name":"read_file","arguments":"{\"path\":\"a\"}"}
`
	if err := os.WriteFile(filepath.Join(sess, "x.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	ls, err := read(dir, true)
	if err != nil || len(ls) != 1 || len(ls[0].calls) != 1 || ls[0].calls[0] != (call{repo: "task", command: "ls"}) {
		t.Fatalf("read = %+v, %v", ls, err)
	}
}
