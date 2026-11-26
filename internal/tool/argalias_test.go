package tool

import (
	"encoding/json"
	"testing"
)

func TestNormalizeArgsRenamesBashCmd(t *testing.T) {
	got := NormalizeArgs("bash", json.RawMessage(`{"cmd":"ls","timeout_ms":5}`))
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	if m["command"] != "ls" || m["cmd"] != nil || m["timeout_ms"] != float64(5) {
		t.Fatalf("got %s", got)
	}
	// The real name wins over the alias.
	got = NormalizeArgs("bash", json.RawMessage(`{"cmd":"rm -rf x","command":"ls"}`))
	if err := json.Unmarshal(got, &m); err != nil || m["command"] != "ls" || m["cmd"] != nil {
		t.Fatalf("got %s", got)
	}
	// Other tools and non-objects pass through untouched.
	in := json.RawMessage(`{"cmd":"x"}`)
	if string(NormalizeArgs("read_file", in)) != string(in) {
		t.Fatal("read_file input changed")
	}
	if string(NormalizeArgs("bash", json.RawMessage(`"ls"`))) != `"ls"` {
		t.Fatal("non-object changed")
	}
}
