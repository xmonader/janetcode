package fixcache

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	root := "/home/u/proj"
	for line, want := range map[string]string{
		"/home/u/proj/loader/loader.go:12:5: undefined: time":                          "loader/loader.go: undefined: time",
		"./loader/loader.go:40:2: undefined: time":                                     "./loader/loader.go: undefined: time",
		"NameError: name 'parse_date' is not defined":                                  "NameError: name 'parse_date' is not defined",
		"  File \"/tmp/pytest-of-u/pytest-3/m.py\", line 7, in f":                      "", // a frame, not an error
		"panic: runtime error: index out of range [3] with length 3":                   "panic: runtime error: index out of range [N] with length N",
		"fatal error: sync: unlock of unlocked mutex 0xc000123456":                     "fatal error: sync: unlock of unlocked mutex ADDR",
		"error[E0308]: mismatched types":                                               "error[EN]: mismatched types",
		"\x1b[31mTypeError: Cannot read properties of undefined (reading 'id')\x1b[0m": "TypeError: Cannot read properties of undefined (reading 'id')",
		// Lines that say only that something failed are not signatures.
		"FAIL":                        "",
		"--- FAIL: TestCache (0.01s)": "",
		"exit status 1":               "",
		"Error: Process completed with exit code 1.":                "",
		"make: *** [Makefile:12: test] Error 2":                     "",
		"=========== 2 failed, 3 passed in 0.12s ====":              "",
		"FAILED (failures=2, errors=1)":                             "",
		"Traceback (most recent call last):":                        "",
		"[exit code 1]":                                             "",
		"E   assert 3 == 4":                                         "", // one word: names no particular error
		"collected 5 items":                                         "",
		"error: could not compile `store` due to 2 previous errors": "",
	} {
		if got := Normalize(line, root); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestSignaturesDistinctAndBounded(t *testing.T) {
	out := strings.Join([]string{
		"# m",
		"m/a.go:1:1: undefined: foo",
		"m/a.go:9:1: undefined: foo", // the same error on another line
		"m/b.go:2:1: undefined: bar",
		"m/c.go:2:1: undefined: baz",
		"m/d.go:2:1: undefined: qux",
		"m/e.go:2:1: undefined: quux",
		"FAIL",
	}, "\n")
	got := Signatures(out, "")
	want := []string{"m/a.go: undefined: foo", "m/b.go: undefined: bar", "m/c.go: undefined: baz", "m/d.go: undefined: qux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Signatures = %q, want %q", got, want)
	}
	if got := Signatures("FAIL\nexit status 1\n", ""); len(got) != 0 {
		t.Fatalf("a bare failure has signatures %q", got)
	}
}

func TestStoreAddLookup(t *testing.T) {
	s := &Store{}
	if s.Add(Entry{Fix: "x"}) || s.Add(Entry{Signatures: []string{"a b"}, Fix: "  "}) {
		t.Fatal("an entry with no signature or no fix was recorded")
	}
	s.Add(Entry{Signatures: []string{"undefined: foo", "undefined: bar"}, Fix: "old", Session: "s1"})
	s.Add(Entry{Signatures: []string{"undefined: bar", "undefined: foo"}, Fix: "new", Session: "s2"})
	if len(s.Entries) != 1 || s.Entries[0].Fix != "new" {
		t.Fatalf("the same signatures were kept twice: %+v", s.Entries)
	}
	if _, _, ok := s.Lookup([]string{"undefined: foo"}, "s2"); ok {
		t.Fatal("the session that recorded the fix was answered with it")
	}
	e, shared, ok := s.Lookup([]string{"something else", "undefined: foo"}, "s3")
	if !ok || e.Fix != "new" || shared != "undefined: foo" {
		t.Fatalf("Lookup = %+v %q %v", e, shared, ok)
	}
	if _, _, ok := s.Lookup([]string{"undefined: baz"}, "s3"); ok {
		t.Fatal("an unrelated signature matched")
	}
	long := &Store{}
	long.Add(Entry{Signatures: []string{"x y"}, Fix: strings.Repeat("a", MaxFixBytes+50)})
	if n := len(long.Entries[0].Fix); n > MaxFixBytes+10 {
		t.Fatalf("fix kept at %d bytes", n)
	}
	for i := 0; i < MaxEntries+5; i++ {
		long.Add(Entry{Signatures: []string{strings.Repeat("s", i+1) + " sig"}, Fix: "f"})
	}
	if len(long.Entries) != MaxEntries {
		t.Fatalf("entries = %d, want %d", len(long.Entries), MaxEntries)
	}
}

func TestStoreSaveOpen(t *testing.T) {
	dir := t.TempDir()
	p := PathFor(filepath.Join(dir, "fixcache"), "/home/u/proj")
	if p != PathFor(filepath.Join(dir, "fixcache"), "/home/u/proj/") || p == PathFor(filepath.Join(dir, "fixcache"), "/home/u/other") {
		t.Fatal("PathFor does not key on the cleaned root")
	}
	s, err := Open(p)
	if err != nil || len(s.Entries) != 0 {
		t.Fatalf("a missing store: %+v %v", s, err)
	}
	s.Add(Entry{Signatures: []string{"undefined: foo"}, Fix: "+x", Files: []string{"a.go"}})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	back, err := Open(p)
	if err != nil || len(back.Entries) != 1 || back.Entries[0].Files[0] != "a.go" {
		t.Fatalf("reopened: %+v %v", back, err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if bad, err := Open(p); err == nil || bad == nil || len(bad.Entries) != 0 {
		t.Fatalf("a corrupt store: %+v %v", bad, err)
	}
}
