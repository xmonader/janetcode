package prompt

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMentionPathsQuoted(t *testing.T) {
	got := mentionPaths(`see @"my dir/a b.go", @plain.go. and mail@example.com @"" then @"unclosed`)
	want := []string{"my dir/a b.go", "plain.go", "unclosed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A path with a space in it attaches when written the way completion
// inserts it.
func TestExpandMentionsQuotedPathWithSpace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "my notes.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	tok, ok := Mention("my notes.md")
	if !ok || tok != `@"my notes.md"` {
		t.Fatalf("Mention = %q %v", tok, ok)
	}
	out, atts := ExpandMentions(dir, "read "+tok+" please")
	if len(atts) != 1 || atts[0].Path != "my notes.md" || !strings.Contains(out, "hello") {
		t.Fatalf("atts = %+v\n%s", atts, out)
	}
}

func TestMentionTokens(t *testing.T) {
	if tok, _ := Mention("a.go"); tok != "@a.go" {
		t.Fatalf("bare = %q", tok)
	}
	if _, ok := Mention(`we"ird`); ok {
		t.Fatal("a path with a quote has no usable mention")
	}
}

func TestMentionAtEnd(t *testing.T) {
	for _, c := range []struct {
		v       string
		partial string
		start   int
		quoted  bool
		ok      bool
	}{
		{"explain @ma", "ma", 8, false, true},
		{"@", "", 0, false, true},
		{`explain @"my dir/th`, "my dir/th", 8, true, true},
		{`explain @"my dir/the notes.go" `, "", 0, false, false},
		{`explain @"my dir/the notes.go"`, "", 0, false, false},
		{"mail@ex", "", 0, false, false},
		{"plain words", "", 0, false, false},
	} {
		p, s, q, ok := MentionAtEnd(c.v)
		if p != c.partial || s != c.start || q != c.quoted || ok != c.ok {
			t.Errorf("MentionAtEnd(%q) = %q %d %v %v", c.v, p, s, q, ok)
		}
	}
}
