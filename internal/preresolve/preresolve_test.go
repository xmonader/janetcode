package preresolve

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/repomap"
)

// preresolveRepo writes files under a temp root and indexes it.
func preresolveRepo(t *testing.T, files map[string]string) *repomap.Index {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := repomap.Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestMentionsFindsPathsAndCodeShapedNames(t *testing.T) {
	got := Mentions("Fix `Cache.Get` in internal/cache/cache.go so parseConfig and max_retries agree. The Server should use `Flush()` and see config.yaml:12. Also e.g. HTTP.")
	want := []Mention{
		{"Cache.Get", KindIdent},
		{"Flush", KindIdent},
		{"internal/cache/cache.go", KindPath},
		{"config.yaml", KindPath},
		{"parseConfig", KindIdent},
		{"max_retries", KindIdent},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mentions\n got %v\nwant %v", got, want)
	}
}

func TestMentionsIgnoresProse(t *testing.T) {
	// A capitalised sentence start, an acronym, an abbreviation and a
	// version number are not names.
	if got := Mentions("The Server is slow. JSON output, i.e. the report, broke in v1.2 for Users."); len(got) != 0 {
		t.Fatalf("prose read as names: %v", got)
	}
}

const preresolveCacheGo = `package cache

import "time"

// Cache holds values for a while.
type Cache struct {
	ttl time.Duration
	m   map[string]string
}

// Get returns the value for key, if it is still fresh.
func (c *Cache) Get(key string) (string, bool) {
	v, ok := c.m[key]
	if !ok {
		return "", false
	}
	return v, true
}

const (
	// DefaultTTL is how long a value lives.
	DefaultTTL = time.Minute
	other      = 2
)
`

const preresolveCacheTest = `package cache

import "testing"

func TestGetMissing(t *testing.T) {
	c := &Cache{m: map[string]string{}}
	if _, ok := c.Get("x"); ok {
		t.Fatal("found")
	}
}

func TestUnrelated(t *testing.T) {
	t.Log("nothing here")
}
`

func TestResolveShowsTheDefinitionAndItsTest(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{
		"cache/cache.go":      preresolveCacheGo,
		"cache/cache_test.go": preresolveCacheTest,
		"main.go":             "package main\n\nfunc main() {}\n",
	})
	res := Resolve(ix, "Make `Cache.Get` drop entries older than the TTL.", 0)
	if res.Text == "" {
		t.Fatal("nothing resolved")
	}
	for _, want := range []string{
		`<definition symbol="Cache.Get" path="cache/cache.go" lines="11-18">`,
		"// Get returns the value for key",
		`return v, true`,
		`<test covers="Cache.Get" path="cache/cache_test.go" lines="5-10">`,
		"func TestGetMissing",
	} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("missing %q in\n%s", want, res.Text)
		}
	}
	if strings.Contains(res.Text, "TestUnrelated") {
		t.Errorf("a test that does not mention the symbol was shown:\n%s", res.Text)
	}
	if strings.Contains(res.Text, "type Cache struct") {
		t.Errorf("the excerpt ran past the method into the rest of the file:\n%s", res.Text)
	}
	if len(res.Whole) != 0 {
		t.Errorf("an excerpt was recorded as a whole file: %v", res.Whole)
	}
}

func TestResolveOneSpecOfAGroupedConst(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{"cache/cache.go": preresolveCacheGo, "main.go": "package main\n"})
	res := Resolve(ix, "Raise DefaultTTL to five minutes", 0)
	if !strings.Contains(res.Text, "// DefaultTTL is how long a value lives.\n\tDefaultTTL = time.Minute\n</definition>") {
		t.Fatalf("want the one spec with its comment, got\n%s", res.Text)
	}
}

func TestResolveSkipsGenericNames(t *testing.T) {
	files := map[string]string{}
	for _, p := range []string{"a", "b", "c", "d"} {
		files[p+"/x.go"] = "package " + p + "\n\nfunc NewThing() {}\n"
	}
	ix := preresolveRepo(t, files)
	if res := Resolve(ix, "Why does NewThing allocate?", 0); res.Text != "" {
		t.Fatalf("a name four files define was resolved:\n%s", res.Text)
	}
}

func TestResolveNamedFileWholeOrOutline(t *testing.T) {
	big := "package big\n\n" + strings.Repeat("// filler line to make the file large enough\n", 200) + "func Large() {}\n"
	ix := preresolveRepo(t, map[string]string{
		"small/s.go": "package small\n\nfunc Small() {}\n",
		"big/b.go":   big,
	})
	res := Resolve(ix, "Look at s.go and big/b.go", 0)
	if _, ok := res.Whole["small/s.go"]; !ok {
		t.Fatalf("the small file was not sent whole: %v\n%s", res.Whole, res.Text)
	}
	if _, ok := res.Whole["big/b.go"]; ok {
		t.Fatal("a file over the whole-file limit was sent whole")
	}
	if !strings.Contains(res.Text, `<outline path="big/b.go"`) || !strings.Contains(res.Text, "func Large") {
		t.Fatalf("no outline for the big file:\n%s", res.Text)
	}
}

func TestResolveAmbiguousPathTail(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{"a/util.go": "package a\n", "b/util.go": "package b\n"})
	if res := Resolve(ix, "fix util.go", 0); res.Text != "" {
		t.Fatalf("a tail two files share resolved:\n%s", res.Text)
	}
}

func TestResolveKeepsToTheBudget(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{
		"cache/cache.go":      preresolveCacheGo,
		"cache/cache_test.go": preresolveCacheTest,
		"main.go":             "package main\n",
	})
	res := Resolve(ix, "`Cache.Get` and `DefaultTTL`", 400)
	if len(res.Text) > 400 {
		t.Fatalf("%d bytes over a 400-byte budget", len(res.Text))
	}
	if res.Dropped == 0 {
		t.Fatal("nothing was dropped although everything could not fit")
	}
	if res.Text != "" && !strings.Contains(res.Text, "DefaultTTL") {
		t.Fatalf("the item that fits should still be sent:\n%s", res.Text)
	}
}

func TestResolveWordTier(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{
		"billing/limits.go": "package billing\n\n// RefundWindowDays is how long a refund may be requested.\nconst RefundWindowDays = 45\n",
		"billing/a.go":      "package billing\n\nfunc OrderTotal() int { return 1 }\n",
		"cart/a.go":         "package cart\n\nfunc OrderCount() int { return 1 }\n",
		"cart/b.go":         "package cart\n\nfunc OrderLines() int { return 1 }\n",
		"cart/c.go":         "package cart\n\nfunc OrderTax() int { return 1 }\n",
	})
	res := Resolve(ix, "how long may a refund be requested after an order?", 0)
	if !strings.Contains(res.Text, `symbol="RefundWindowDays"`) {
		t.Fatalf("the word refund did not find RefundWindowDays:\n%s", res.Text)
	}
	// "order" is a part of four definitions in three files: a theme, not
	// a pointer.
	if strings.Contains(res.Text, "Order") {
		t.Fatalf("a word four definitions share was resolved:\n%s", res.Text)
	}
}

// The word tier's false positives, taken from recorded prompts that name
// no code: a word two definitions share, and a definition whose whole
// name is one English word.
func TestResolveWordTierLeavesEnglishAlone(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{
		"web/a.go":    "package web\n\nfunc applyHello() {}\n",
		"web/b.go":    "package web\n\nfunc helloPayload() {}\n",
		"init/s.go":   "package init\n\nfunc Survey() {}\n",
		"stats/st.go": "package stats\n\nfunc hasRemoteContaining() {}\nfunc rootContaining() {}\n",
	})
	for _, prompt := range []string{
		"Reply with exactly: hello world. Do not use any tools.",
		"Run a quick survey of the layout",
		"Create hello.txt containing exactly: hi",
	} {
		if res := Resolve(ix, prompt, 0); res.Text != "" {
			t.Errorf("%q resolved %v", prompt, res.Symbols)
		}
	}
}

func TestResolveNothingNamed(t *testing.T) {
	ix := preresolveRepo(t, map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	if res := Resolve(ix, "explain the architecture", 0); res.Text != "" {
		t.Fatalf("resolved something from nothing:\n%s", res.Text)
	}
}

func TestExtentPythonAndBraces(t *testing.T) {
	py := strings.Split(`import os

# Load the config.
@cached
def load(path,
         strict=False):
    data = open(path).read()

    return data

def other():
    pass`, "\n")
	if from, to := Extent("x.py", py, 4); from != 2 || to != 8 {
		t.Errorf("python extent = %d-%d, want 2-8", from, to)
	}
	ts := strings.Split(`// adds
export function add(a: number, b: number): number {
  const s = "}";
  return a + b;
}
export const x = 1;`, "\n")
	if from, to := Extent("x.ts", ts, 1); from != 0 || to != 4 {
		t.Errorf("ts extent = %d-%d, want 0-4 (a brace in a string must not close it)", from, to)
	}
	if from, to := Extent("x.ts", ts, 5); from != 5 || to != 5 {
		t.Errorf("one-line const = %d-%d, want 5-5", from, to)
	}
}

func TestExtentIsCapped(t *testing.T) {
	ls := []string{"func f() {"}
	for i := 0; i < 200; i++ {
		ls = append(ls, "\tx()")
	}
	ls = append(ls, "}")
	if from, to := Extent("x.c", ls, 0); to-from+1 != maxDefLines {
		t.Fatalf("extent %d-%d is not capped at %d lines", from, to, maxDefLines)
	}
}
