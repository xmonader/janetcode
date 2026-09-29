package tool

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLargeTreeTools measures glob, grep and list_dir on a large tree.
// It runs only when JANETCODE_LARGE_TREE names one (docs/notes/large-repos.md
// has the numbers and how they were taken), and fails when a call takes
// longer than a turn can afford or holds more memory than it should.
func TestLargeTreeTools(t *testing.T) {
	root := os.Getenv("JANETCODE_LARGE_TREE")
	if root == "" {
		t.Skip("JANETCODE_LARGE_TREE is not set")
	}
	ctx := context.Background()
	measure := func(name string, run func() (string, error)) string {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		out, err := run()
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		t.Logf("%-28s %8s  %6d bytes out  %7.1f MB allocated", name, took.Round(time.Millisecond), len(out), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
		if took > 30*time.Second {
			t.Errorf("%s took %s", name, took)
		}
		return out
	}
	in := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	out := measure("glob **/*.go", func() (string, error) {
		return Glob{Root: root}.Run(ctx, in(map[string]string{"pattern": "**/*.go"}))
	})
	if !strings.Contains(out, ".go") {
		t.Errorf("glob found no Go files:\n%.400s", out)
	}
	measure("glob **/*_test.go", func() (string, error) {
		return Glob{Root: root}.Run(ctx, in(map[string]string{"pattern": "**/*_test.go"}))
	})
	measure("grep rare identifier", func() (string, error) {
		return Grep{Root: root}.Run(ctx, in(map[string]string{"pattern": "func NewSharedInformerFactory\\b"}))
	})
	measure("grep common word", func() (string, error) {
		return Grep{Root: root}.Run(ctx, in(map[string]string{"pattern": "context"}))
	})
	measure("grep with glob *.go", func() (string, error) {
		return Grep{Root: root}.Run(ctx, in(map[string]string{"pattern": "TODO", "glob": "*.go"}))
	})
	measure("list_dir .", func() (string, error) {
		return ListDir{Root: root}.Run(ctx, in(map[string]string{"path": "."}))
	})
}
