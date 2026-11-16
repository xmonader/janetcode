package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func requireData(t *testing.T, data json.RawMessage, want string) {
	t.Helper()
	var gotJSON, wantJSON any
	if err := json.Unmarshal(data, &gotJSON); err != nil {
		t.Fatalf("invalid structured data %q: %v", data, err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("data = %s; want %s", data, want)
	}
}

// A rich read must return the window from its first read, with no extra
// read to reconstruct data after rendering. This also exercises workspace IO.
type singleReadHost struct {
	Host
	reads int
}

func (h *singleReadHost) ReadFile(ctx context.Context, path string) ([]byte, error) {
	h.reads++
	if h.reads > 1 {
		return nil, fmt.Errorf("unexpected second read")
	}
	return h.Host.ReadFile(ctx, path)
}

func TestReadFileStructuredWindow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("first\n12\ttext\nthird\nfourth\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := NewLocal(root)
	host := &singleReadHost{Host: ws.Host()}
	ws.host = host
	r, err := (ReadFile{Root: root, WS: ws}).RunRich(context.Background(), raw(`{"path":"a.txt","offset":2,"limit":2,"anchors":true}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, r.Data, `{"path":"a.txt","offset":2,"lines":["12\ttext","third"],"total_lines":4,"truncated":true}`)
	if host.reads != 1 || !strings.Contains(r.Text, Anchor(2, "12\ttext")+"\t12\ttext\n") {
		t.Fatalf("reads=%d, display=%q", host.reads, r.Text)
	}
}

func TestReadFileStructuredEmptyAndErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	reader := ReadFile{Root: root}
	r, err := reader.RunRich(context.Background(), raw(`{"path":"empty"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, r.Data, `{"path":"empty","offset":1,"lines":[],"total_lines":0,"truncated":false}`)
	for _, in := range []string{`{"path":"missing"}`, `{"path":"../outside"}`, `{"path":"empty","bogus":true}`} {
		r, err := reader.RunRich(context.Background(), raw(in))
		if err == nil || len(r.Data) != 0 {
			t.Fatalf("%s: result=%+v err=%v", in, r, err)
		}
	}
}

func TestSearchStructuredData(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a:b.go"), []byte("skip\n12\tneedle: data\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := (Glob{Root: root}).RunRich(context.Background(), raw(`{"pattern":"*.go","path":"pkg"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, g.Data, `{"path":"pkg","paths":["a:b.go"],"truncated":false}`)
	grep := Grep{Root: root}
	r, err := grep.RunRich(context.Background(), raw(`{"pattern":"needle","path":"pkg"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, r.Data, `{"path":"pkg","matches":[{"path":"a:b.go","line":2,"text":"12\tneedle: data","truncated":false}],"truncated":false,"partially_searched_lines":0}`)
	if r.Text != "a:b.go:2:12\tneedle: data\n" {
		t.Fatalf("display changed: %q", r.Text)
	}
	r, err = grep.RunRich(context.Background(), raw(`{"pattern":"absent"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, r.Data, `{"path":".","matches":[],"truncated":false,"partially_searched_lines":0}`)
	g, err = (Glob{Root: root}).RunRich(context.Background(), raw(`{"pattern":"*.missing"}`))
	if err != nil {
		t.Fatal(err)
	}
	requireData(t, g.Data, `{"path":".","paths":[],"truncated":false}`)
}

func TestGrepStructuredLimits(t *testing.T) {
	root := t.TempDir()
	content := "needle" + strings.Repeat("x", maxScanLine) + "\n" + strings.Repeat("needle\n", maxGrepResults)
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := (Grep{Root: root}).RunRich(context.Background(), raw(`{"pattern":"needle","path":"long.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var data GrepData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Matches) != maxGrepResults || !data.Truncated || data.PartiallySearchedLines != 1 {
		t.Fatalf("matches=%d truncated=%t partial=%d", len(data.Matches), data.Truncated, data.PartiallySearchedLines)
	}
	if !data.Matches[0].Truncated || len(data.Matches[0].Text) > maxGrepLine || data.Matches[1].Truncated {
		t.Fatalf("match bounds wrong: first=%+v second=%+v", data.Matches[0], data.Matches[1])
	}
}

func TestGlobStructuredLimit(t *testing.T) {
	root := t.TempDir()
	for i := 0; i <= maxGlobResults; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%04d.txt", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r, err := (Glob{Root: root}).RunRich(context.Background(), raw(`{"pattern":"*.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	var data GlobData
	if err := json.Unmarshal(r.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Paths) != maxGlobResults || !data.Truncated {
		t.Fatalf("paths=%d truncated=%t", len(data.Paths), data.Truncated)
	}
}
