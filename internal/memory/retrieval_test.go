package memory

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestExpandQuery(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"that thing we discussed about the API", []string{"api"}},
		{"what is parseConfig doing", []string{"parseconfig", "doing", "parse", "config"}},
		{"snake_case_name HTTPServer", []string{"snake_case_name", "httpserver", "snake", "case", "name", "http", "server"}},
		{"", nil},
	} {
		if got := ExpandQuery(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("ExpandQuery(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestChunk(t *testing.T) {
	short := "just a note"
	if got := Chunk(short, 100); len(got) != 1 || got[0] != short {
		t.Fatalf("short = %q", got)
	}
	long := "# Build\n\n" + strings.Repeat("run make test before pushing. ", 10) + "\n\n# Deploy\n\n" + strings.Repeat("ship via the pipeline. ", 12) + "\n\n" + strings.Repeat("é", 400)
	got := Chunk(long, 200)
	if len(got) < 4 {
		t.Fatalf("want several chunks, got %d", len(got))
	}
	for _, c := range got {
		if len(c) > 260 { // size plus a carried heading
			t.Errorf("chunk too big: %d", len(c))
		}
		if c != strings.ToValidUTF8(c, "") {
			t.Errorf("split a rune: %q", c)
		}
	}
	if Chunk("  ", 10) != nil {
		t.Error("blank")
	}
}

func TestDiversifyDropsNearDuplicates(t *testing.T) {
	a := Entry{Name: "a", Description: "run go test before every push to master branch"}
	b := Entry{Name: "b", Description: "run go test before every push to master branch always"}
	c := Entry{Name: "c", Description: "database migrations live in the schema directory"}
	d := Entry{Name: "d", Description: "unrelated"}
	es, rel := []Entry{a, b, c, d}, []float64{10, 9.5, 9, 5}
	got := diversify(es, rel, 2, MMRLambda)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("got %v", names(got))
	}
	if got := diversify(es, rel, 2, 1); got[1].Name != "b" {
		t.Fatalf("lambda 1 must keep order, got %v", names(got))
	}
	if got := diversify(es[:1], rel[:1], 3, MMRLambda); len(got) != 1 {
		t.Fatal("single")
	}
}

func names(es []Entry) []string {
	var o []string
	for _, e := range es {
		o = append(o, e.Name)
	}
	return o
}

func TestRecallGatesAndExpansion(t *testing.T) {
	st := Open(t.TempDir(), t.TempDir())
	st.Put(Entry{Name: "cfg", Description: "config parsing lives in internal/config", Type: TypeProject, Scope: "project", Body: "parse the config with strict decoding", Source: "user"})
	if got := st.Search("why does parseConfig fail", 3); len(got) != 1 {
		t.Fatalf("expanded search = %v", names(got))
	}
	st.NoExpand = true
	if got := st.Search("parseConfig", 3); len(got) != 0 {
		t.Fatalf("unexpanded identifier should not match, got %v", names(got))
	}
	// Near duplicates: MMR keeps the distinct one inside a two-slot recall.
	st = Open(t.TempDir(), t.TempDir())
	for _, e := range []Entry{
		{Name: "t1", Description: "always run the integration suite before release builds", Body: "integration suite release builds checklist"},
		{Name: "t2", Description: "always run the integration suite before release builds again", Body: "integration suite release builds checklist twice"},
		{Name: "t3", Description: "release builds are signed by the packaging script", Body: "signing packaging script"},
	} {
		e.Type, e.Scope, e.Source = TypeProject, "project", "user"
		if _, _, err := st.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	got := st.Search("release builds integration suite signing", 2)
	if len(got) != 2 || (got[0].Name != "t3" && got[1].Name != "t3") {
		t.Fatalf("mmr: %v", names(got))
	}
	st.NoMMR = true
	got = st.Search("release builds integration suite signing", 2)
	if len(got) != 2 || got[0].Name == "t3" && got[1].Name == "t3" {
		t.Fatalf("plain: %v", names(got))
	}
}

func TestLongEntryEmbedsPerChunk(t *testing.T) {
	st := Open(t.TempDir(), t.TempDir())
	body := strings.Repeat("filler about nothing in particular. ", 30) + "\n\n## Deploy\n\n" + strings.Repeat("rollout uses canary. ", 20)
	e := Entry{Name: "long", Description: "ops notes", Type: TypeProject, Scope: "project", Body: body, Source: "user"}
	if _, _, err := st.Put(e); err != nil {
		t.Fatal(err)
	}
	if n := len(st.embedChunks(e)); n < 2 {
		t.Fatalf("chunks = %d", n)
	}
	st.Embed = func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i, tx := range texts {
			v := []float32{0, 1}
			if strings.Contains(tx, "canary") || strings.Contains(tx, "deploy") {
				v = []float32{1, 0}
			}
			out[i] = v
		}
		return out, nil
	}
	got := st.SearchCtx(context.Background(), "canary", 3)
	if len(got) != 1 {
		t.Fatalf("got %v", names(got))
	}
	st.NoChunk = true
	if n := len(st.embedChunks(e)); n != 1 {
		t.Fatalf("NoChunk chunks = %d", n)
	}
}
