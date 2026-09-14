package verify

import "testing"

// TestCleanCommand: the startup tip itself suggested /verify set "go test
// ./...", and the quotes were stored literally, so every run failed with
// `bash: go test ./...: No such file or directory`. One pair of matching
// quotes or backticks around the whole command comes off; quotes that
// belong to the command stay.
func TestCleanCommand(t *testing.T) {
	for in, want := range map[string]string{
		`"go test ./..."`:       "go test ./...",
		`'make test'`:           "make test",
		"`go vet ./...`":        "go vet ./...",
		"```go test ./...```":   "go test ./...",
		`  go test ./...  `:     "go test ./...",
		`"a" && "b"`:            `"a" && "b"`,
		`grep -q "ok" a.txt`:    `grep -q "ok" a.txt`,
		`echo 'x'`:              `echo 'x'`,
		`""`:                    "",
		"\"go test\nmake\"":     "",
		`"it's fine"`:           "it's fine",
		`sh -c "go test ./..."`: `sh -c "go test ./..."`,
	} {
		if got := CleanCommand(in); got != want {
			t.Errorf("CleanCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestEditSetReplacesAddAppends: /verify set used to append, so a second
// set (to fix a wrong command) kept the wrong one running first.
func TestEditSetReplacesAddAppends(t *testing.T) {
	c := Config{Commands: []string{`"go test ./..."`}, Mutate: "go test ./..."}
	c, note, err := Edit(c, "set", `"go test ./..."`)
	if err != nil || len(c.Commands) != 1 || c.Commands[0] != "go test ./..." {
		t.Fatalf("set: %+v %q %v", c, note, err)
	}
	c, _, err = Edit(c, "add", "go vet ./...")
	if err != nil || len(c.Commands) != 2 || c.Commands[1] != "go vet ./..." {
		t.Fatalf("add: %+v %v", c, err)
	}
	if c.Mutate != "go test ./..." {
		t.Fatalf("set dropped mutate: %+v", c)
	}
	if _, _, err := Edit(c, "set", `""`); err == nil {
		t.Fatal("set with an empty command was accepted")
	}
	c, _, err = Edit(c, "mutate", "off")
	if err != nil || c.Mutate != "" {
		t.Fatalf("mutate off: %+v %v", c, err)
	}
	c, _, err = Edit(c, "clear", "")
	if err != nil || len(c.Commands) != 0 {
		t.Fatalf("clear: %+v %v", c, err)
	}
	if _, _, err := Edit(c, "bogus", ""); err == nil {
		t.Fatal("unknown verb accepted")
	}
}
