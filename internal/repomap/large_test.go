package repomap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSelectFilesSpansTheTree: a tree over the limit used to be indexed
// by its first files in sorted order, so everything past the letter the
// limit reached was invisible to the map. The selection now takes
// changed files first, then code, then tests, then generated code, and
// samples a tier that does not fit evenly across it.
func TestSelectFilesSpansTheTree(t *testing.T) {
	var paths []string
	for _, dir := range []string{"api", "cmd", "pkg", "staging", "zzz"} {
		for i := 0; i < 100; i++ {
			paths = append(paths, fmt.Sprintf("%s/f%03d.go", dir, i))
			paths = append(paths, fmt.Sprintf("%s/f%03d_test.go", dir, i))
			paths = append(paths, fmt.Sprintf("%s/zz_generated.f%03d.go", dir, i))
		}
	}
	got := selectFiles(paths, []string{"zzz/f099_test.go"}, 100)
	if len(got) != 100 {
		t.Fatalf("selected %d, want 100", len(got))
	}
	perDir := map[string]int{}
	has := map[string]bool{}
	for _, p := range got {
		perDir[strings.Split(p, "/")[0]]++
		has[p] = true
		if strings.Contains(p, "zz_generated") {
			t.Errorf("generated %s chosen while code was left out", p)
		}
	}
	if !has["zzz/f099_test.go"] {
		t.Error("the changed file was not selected")
	}
	for _, dir := range []string{"api", "cmd", "pkg", "staging", "zzz"} {
		if perDir[dir] < 15 {
			t.Errorf("%s got %d of 100 slots; the sample is not spread across the tree: %v", dir, perDir[dir], perDir)
		}
	}
}

// TestLargeTreeRanksWhatTheRequestNames builds a tree past largeTree in
// which one hub file is used by every other file and one file is named
// for the request. In a large tree the graph's scores used to bury the
// named file under the hubs; it now leads.
func TestLargeTreeRanksWhatTheRequestNames(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("util/hub.go", "package util\n\nfunc HubHelper() {}\nfunc OtherHub() {}\n")
	for i := 0; i < largeTree+100; i++ {
		write(fmt.Sprintf("pkg/p%04d/f.go", i), fmt.Sprintf("package p%04d\n\nfunc Thing%04d() { HubHelper(); OtherHub() }\n", i, i))
	}
	// The request names a directory of twenty files: a word as broad as
	// real ones are, in a real tree, where it is never a single file.
	for i := 0; i < 20; i++ {
		write(fmt.Sprintf("pkg/preemption/part%02d.go", i), fmt.Sprintf("package preemption\n\nfunc Evaluate%02d() {}\n", i))
	}
	ix, err := Build(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Files) <= largeTree {
		t.Fatalf("indexed %d files; the test needs more than %d", len(ix.Files), largeTree)
	}
	ranked := ix.Rank("fix the preemption bug", nil)
	if !strings.HasPrefix(ranked[0].File.Path, "pkg/preemption/") {
		for i, r := range ranked[:5] {
			t.Logf("%d. %s %v %g", i+1, r.File.Path, r.Why, r.Score)
		}
		t.Fatal("the hub outranks every file the request names")
	}
}

// TestEveryDirectoryTheRequestNamesPulls: the first matching path segment
// used to end the match, so a deeper, narrower one never counted.
func TestEveryDirectoryTheRequestNamesPulls(t *testing.T) {
	ix := &Index{Files: []*File{
		{Path: "pkg/kubelet/eviction/eviction_manager.go", refs: map[string]int{}},
		{Path: "pkg/kubelet/other.go", refs: map[string]int{}},
	}, byPath: map[string]int{}, definers: map[string][]int{}}
	ix.link()
	ranked := ix.Rank("kubelet eviction manager memory pressure", nil)
	why := strings.Join(ranked[0].Why, ",")
	if ranked[0].File.Path != "pkg/kubelet/eviction/eviction_manager.go" || !strings.Contains(why, "dir eviction") || !strings.Contains(why, "name manager") {
		t.Fatalf("first = %s %v", ranked[0].File.Path, ranked[0].Why)
	}
}

func TestRenderSaysWhenTheMapIsASelection(t *testing.T) {
	ix := &Index{Files: []*File{{Path: "a.go"}, {Path: "b.go"}}, Total: 30000, byPath: map[string]int{}, definers: map[string][]int{}}
	ix.link()
	m := ix.Render(ix.Rank("x", nil), DefaultBudget)
	if !strings.Contains(m, "2 of 30000 source files") {
		t.Fatalf("header does not say the map is a selection:\n%s", m)
	}
}
