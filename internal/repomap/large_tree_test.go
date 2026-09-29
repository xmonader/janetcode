package repomap

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
)

// TestLargeTreeRepoMap measures the repository map on a large tree. It
// runs only when JANETCODE_LARGE_TREE names one (docs/notes/large-repos.md
// has the numbers and how they were taken).
func TestLargeTreeRepoMap(t *testing.T) {
	root := os.Getenv("JANETCODE_LARGE_TREE")
	if root == "" {
		t.Skip("JANETCODE_LARGE_TREE is not set")
	}
	ctx := context.Background()
	if os.Getenv("JANETCODE_LARGE_GITINIT") != "" {
		// Make the tree a repository, for the git-backed paths (ls-files,
		// the status-based fingerprint).
		for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tree"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	start := time.Now()
	paths, err := listFiles(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("list source files     %8s  %d files", time.Since(start).Round(time.Millisecond), len(paths))

	start = time.Now()
	fp := gitx.TreeFingerprint(root)
	t.Logf("tree fingerprint      %8s  %.20s", time.Since(start).Round(time.Millisecond), fp)

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start = time.Now()
	ix, err := Build(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	t.Logf("build index           %8s  %d files indexed, %.1f MB allocated, %.1f MB live", took.Round(time.Millisecond), len(ix.Files), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), float64(after.HeapAlloc)/(1<<20))

	for _, q := range []string{
		os.Getenv("JANETCODE_LARGE_QUERY"),
		"fix the scheduler's preemption when a pod has a PodDisruptionBudget",
		"kubelet eviction manager memory pressure",
	} {
		if q == "" {
			continue
		}
		start = time.Now()
		ranked := ix.Rank(q, nil)
		m := ix.Render(ranked, DefaultBudget)
		t.Logf("rank+render           %8s  %q (%d bytes)", time.Since(start).Round(time.Millisecond), q, len(m))
		for i, r := range ranked {
			if i < 8 {
				t.Logf("  %d. %s %v %.3g", i+1, r.File.Path, r.Why, r.Score)
			}
			for _, want := range []string{"pkg/scheduler/framework/preemption/preemption.go", "pkg/kubelet/eviction/eviction_manager.go"} {
				if r.File.Path == want {
					t.Logf("  %s at %d %v %.3g", want, i+1, r.Why, r.Score)
				}
			}
		}
	}

	c := &Cache{Root: root}
	start = time.Now()
	if _, err := c.Get(ctx); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	start = time.Now()
	if _, err := c.Get(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("cache cold / warm     %8s / %s", cold.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}
