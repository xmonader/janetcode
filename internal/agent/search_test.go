package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSearchArchiveRanksByAnyTerm(t *testing.T) {
	ar := NewArchive(t.TempDir())
	var build strings.Builder
	for i := 0; i < 300; i++ {
		build.WriteString("compiling package number " + strings.Repeat("x", i%7) + "\n")
	}
	build.WriteString("internal/server/router.go:42:7: undefined: parseTimeout\n")
	build.WriteString("FAIL example.com/svc/server [build failed]\n")
	for i := 0; i < 100; i++ {
		build.WriteString("linking objects\n")
	}
	id1, _ := ar.Put("t1", "bash", build.String())
	id2, _ := ar.Put("t2", "read_file", "package config\n\nconst defaultTimeout = 5 * time.Second\n")

	// A natural-language question: no single line holds every word, and a
	// grep for the whole phrase would return nothing.
	out := searchArchive(ar, nil, "what was the compile error in the router file", 0)
	if !strings.Contains(out, "undefined: parseTimeout") || !strings.Contains(out, id1) {
		t.Fatalf("router error not found:\n%s", out)
	}
	if strings.Index(out, "parseTimeout") > strings.Index(out, "linking objects") && strings.Contains(out, "linking objects") {
		t.Fatalf("filler ranked above the hit:\n%s", out)
	}
	// Restricting to one entry, and a literal anchor.
	out = searchArchive(ar, []string{id2}, "defaultTimeout value", 0)
	if !strings.Contains(out, "5 * time.Second") || strings.Contains(out, id1) {
		t.Fatalf("anchor search:\n%s", out)
	}
	if out := searchArchive(ar, nil, "zzzz qqqq", 0); !strings.HasPrefix(out, "No archived output") {
		t.Fatalf("miss = %q", out)
	}
	if out := searchArchive(ar, nil, "the of", 0); !strings.Contains(out, "no searchable words") {
		t.Fatalf("stopwords = %q", out)
	}
}

func TestExpandOutputQueryMode(t *testing.T) {
	ar := NewArchive(t.TempDir())
	id, _ := ar.Put("t1", "bash", "one\ntwo\nexit status 3: permission denied on /var/lib/app\nfour\n")
	tl := &ExpandOutput{Archive: ar}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"query":"permission denied"}`))
	if err != nil || !strings.Contains(out, "/var/lib/app") || !strings.Contains(out, id) {
		t.Fatalf("query: %v\n%s", err, out)
	}
	out, err = tl.Run(context.Background(), json.RawMessage(`{"id":"`+id+`","query":"exit status"}`))
	if err != nil || !strings.Contains(out, "exit status 3") {
		t.Fatalf("query with id: %v\n%s", err, out)
	}
}

// The model remembers which file it read, not what a line said: naming
// the file in the query must outrank a stray word match elsewhere.
func TestSearchPrefersTheEntryAboutTheSubject(t *testing.T) {
	ar := NewArchive(t.TempDir())
	_, _ = ar.PutSubject("r1", "read_file", "ledger/cli.py", "1\timport argparse\n2\tp.add_argument(\"--top\", type=int)\n")
	id2, _ := ar.PutSubject("r2", "read_file", "ledger/parse.py", "1\timport csv\n2\ttags = [t.strip() for t in row]\n3\treturn Transaction(when=when)\n")
	// Naming the file steers the ranking to its entry: the wanted line
	// comes back and the other file's does not.
	out := searchArchive(ar, nil, "the tags line in ledger/parse.py", 0)
	if !strings.Contains(out, "tags = [t.strip()") || !strings.Contains(out, id2) {
		t.Fatalf("parse.py content not found:\n%s", out)
	}
	if strings.Contains(out, "--top") {
		t.Fatalf("answered from cli.py instead:\n%s", out)
	}
	if es := ar.List(); len(es) != 2 || es[0].Subject != "ledger/parse.py" {
		t.Fatalf("subject not stored: %+v", es)
	}
}

// "Line N of file X" is what a model wants back after a compaction, and
// it is the one question word-ranking cannot answer: the query shares no
// vocabulary with the line. It is looked up directly instead.
func TestSearchAnswersLineOfFile(t *testing.T) {
	ar := NewArchive(t.TempDir())
	read := "1\timport csv\n2\t\n3\tdef parse_row(row):\n4\t    tags = [t.strip() for t in row]\n5\t    return tags\n"
	if _, err := ar.PutSubject("r1", "read_file", "ledger/parse.py", read); err != nil {
		t.Fatal(err)
	}
	out := searchArchive(ar, nil, "line 4 of ledger/parse.py", 0)
	if !strings.Contains(out, "tags = [t.strip()") {
		t.Fatalf("line 4 not returned:\n%s", out)
	}
	if !strings.Contains(out, "> 4\t") {
		t.Fatalf("the wanted line is not marked:\n%s", out)
	}
	// Neighbouring lines come with it, since a caller usually wants the
	// context as well.
	if !strings.Contains(out, "def parse_row") {
		t.Fatalf("no surrounding context:\n%s", out)
	}
	// The other spellings a model uses.
	if o := searchArchive(ar, nil, "ledger/parse.py:3", 0); !strings.Contains(o, "def parse_row") {
		t.Fatalf("path:line form:\n%s", o)
	}
	// A line that is not there falls back to ranking rather than lying.
	if o := searchArchive(ar, nil, "line 99 of ledger/parse.py", 0); strings.Contains(o, "> 99") {
		t.Fatalf("invented a line:\n%s", o)
	}
}

// Inside a compacted conversation many files' reads sit in one entry, so
// line 4 of one file must not be answered with line 4 of another.
func TestLineLookupPicksTheRightFileInACompactedBlob(t *testing.T) {
	ar := NewArchive(t.TempDir())
	blob := `=== message 1 (assistant) ===
[tool call read_file c1] {"path":"a/first.py"}
[tool result c1]
1	FIRST one
2	FIRST two
3	FIRST three
4	FIRST four
=== message 2 (assistant) ===
[tool call read_file c2] {"path":"b/second.py"}
[tool result c2]
1	SECOND one
2	SECOND two
3	SECOND three
4	SECOND four
`
	if _, err := ar.PutSubject("compaction-1", "compacted conversation", "turn 1", blob); err != nil {
		t.Fatal(err)
	}
	out := searchArchive(ar, nil, "line 3 of b/second.py", 0)
	if !strings.Contains(out, "SECOND three") {
		t.Fatalf("wrong file:\n%s", out)
	}
	if strings.Contains(out, "FIRST three") {
		t.Fatalf("answered from the other file too:\n%s", out)
	}
	if o := searchArchive(ar, nil, "line 2 of a/first.py", 0); !strings.Contains(o, "FIRST two") || strings.Contains(o, "SECOND two") {
		t.Fatalf("first file:\n%s", o)
	}
}
