package janetref

import (
	"bytes"
	"compress/zlib"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSessionRefPrescreen is the offline pre-screen for sessions as refs:
// over recorded session files, how many fit in MaxSessionBytes whole, how
// many only once clipped (and how hard), how many not at all, and what
// the saved transcript costs in the repository once git has compressed
// it (zlib, as a loose object). Skipped unless
// JANETCODE_SESSIONREF_PRESCREEN names a directory to walk:
//
//	JANETCODE_SESSIONREF_PRESCREEN=docs/benchmarks/effbench/runs go test ./internal/janetref -run SessionRefPrescreen -v
func TestSessionRefPrescreen(t *testing.T) {
	root := os.Getenv("JANETCODE_SESSIONREF_PRESCREEN")
	if root == "" {
		t.Skip("JANETCODE_SESSIONREF_PRESCREEN not set")
	}
	var raws, stored []int
	clips := map[int]int{}
	failed := 0
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil || !bytes.HasPrefix(raw, []byte(`{"type":"meta"`)) {
			return nil // not a session file (proxy logs, results)
		}
		raws = append(raws, len(raw))
		out, clip, err := FitTranscript(raw, MaxSessionBytes-1024)
		if err != nil {
			failed++
			return nil
		}
		clips[clip]++
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write(out)
		w.Close()
		stored = append(stored, z.Len())
		return nil
	})
	if len(raws) == 0 {
		t.Skip("no session files under " + root)
	}
	q := func(xs []int, f float64) int {
		s := append([]int(nil), xs...)
		sort.Ints(s)
		return s[min(len(s)-1, int(f*float64(len(s))))]
	}
	t.Logf("%d sessions; raw transcript KB p50 %d, p90 %d, max %d", len(raws), q(raws, .5)>>10, q(raws, .9)>>10, q(raws, 1)>>10)
	t.Logf("fit under %d KB: whole %d, clipped to 16K %d, 4K %d, 1K %d, 256B %d, not at all %d",
		MaxSessionBytes>>10, clips[0], clips[16<<10], clips[4<<10], clips[1<<10], clips[256], failed)
	if len(stored) > 0 {
		t.Logf("saved transcript compressed KB p50 %d, p90 %d, max %d", q(stored, .5)>>10, q(stored, .9)>>10, q(stored, 1)>>10)
	}
}
