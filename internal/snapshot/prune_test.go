package snapshot

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// gitIn runs git in root and returns its output.
func gitIn(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// plantRef anchors a fresh commit dated age ago under ref and returns the
// commit id.
func plantRef(t *testing.T, root, ref string, age time.Duration) string {
	t.Helper()
	when := time.Now().Add(-age).Format(time.RFC3339)
	cmd := exec.Command("git", "commit-tree", "HEAD^{tree}", "-m", ref)
	cmd.Dir = root
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	c := strings.TrimSpace(string(out))
	gitIn(t, root, "update-ref", ref, c)
	return c
}

func refExists(t *testing.T, root, ref string) bool {
	t.Helper()
	return exec.Command("git", "-C", root, "rev-parse", "-q", "--verify", ref).Run() == nil
}

func TestPruneKeepsReferencedAndYoungDeletesOld(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	repo, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	plantRef(t, root, RefPrefix+"old", 20*day)
	plantRef(t, root, RefPrefix+"kept", 20*day)
	plantRef(t, root, RefPrefix+"young", day)
	plantRef(t, root, "refs/heads/janet-keep", 90*day)
	plantRef(t, root, "refs/janetcode/other/x", 90*day)
	n, err := repo.Prune(ctx, map[string]bool{"kept": true}, DefaultPrunePolicy(), time.Now())
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	for ref, want := range map[string]bool{
		RefPrefix + "old": false, RefPrefix + "kept": true, RefPrefix + "young": true,
		"refs/heads/janet-keep": true, "refs/janetcode/other/x": true,
	} {
		if got := refExists(t, root, ref); got != want {
			t.Errorf("%s exists = %v, want %v", ref, got, want)
		}
	}
}

func TestPruneCapsYoungUnreferencedRefsAtNewest(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	repo, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	// Ten refs, an hour apart, the newest first; keep three.
	for i := 0; i < 10; i++ {
		plantRef(t, root, fmt.Sprintf("%sr%d", RefPrefix, i), time.Duration(i+1)*time.Hour)
	}
	plantRef(t, root, RefPrefix+"pinned", 2*time.Hour*24*3)
	n, err := repo.Prune(ctx, map[string]bool{"pinned": true}, PrunePolicy{MaxAge: 7 * 24 * time.Hour, Keep: 3}, time.Now())
	if err != nil || n != 7 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	for i := 0; i < 10; i++ {
		ref := fmt.Sprintf("%sr%d", RefPrefix, i)
		if got := refExists(t, root, ref); got != (i < 3) {
			t.Errorf("%s exists = %v", ref, got)
		}
	}
	if !refExists(t, root, RefPrefix+"pinned") {
		t.Error("a referenced ref was counted against the cap")
	}
}

func TestPruneAfterTakeKeepsTheFreshSnapshot(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	repo, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := repo.Take(ctx, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := repo.Prune(ctx, nil, DefaultPrunePolicy(), time.Now()); err != nil || n != 0 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	if !refExists(t, root, RefPrefix+s.Commit) {
		t.Fatal("fresh snapshot ref was deleted")
	}
}
