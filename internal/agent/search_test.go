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
	out := searchArchive(ar, nil, "ledger/parse.py line 2", 0)
	if !strings.HasPrefix(strings.SplitN(out, "\n", 3)[2], "\n"+id2) && !strings.Contains(strings.SplitN(out, "\n\n", 2)[1], id2+" (read_file ledger/parse.py)") {
		t.Fatalf("parse.py entry not first:\n%s", out)
	}
	if !strings.Contains(out, "tags = [t.strip()") {
		t.Fatalf("line 2 of parse.py missing:\n%s", out)
	}
	if es := ar.List(); len(es) != 2 || es[0].Subject != "ledger/parse.py" {
		t.Fatalf("subject not stored: %+v", es)
	}
}
