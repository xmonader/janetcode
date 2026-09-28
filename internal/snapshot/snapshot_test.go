package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a1\n"), 0o644)
	os.WriteFile(filepath.Join(root, "c.txt"), []byte("c1\n"), 0o644)
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("ignored.log\n"), 0o644)
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	return root
}

func write(t *testing.T, p, s string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(p string) string { b, _ := os.ReadFile(p); return string(b) }

func status(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestTakeAndRestoreCoversUntrackedAndDeleted(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	r, err := Open(context.Background(), filepath.Join(root))
	if err != nil {
		t.Fatal(err)
	}
	// State to snapshot: a modified, b untracked, c deleted, ignored file.
	write(t, filepath.Join(root, "a.txt"), "a2\n")
	write(t, filepath.Join(root, "b.txt"), "b\n")
	write(t, filepath.Join(root, "ignored.log"), "log\n")
	os.Remove(filepath.Join(root, "c.txt"))
	before := status(t, root)
	s, err := r.Take(ctx, "turn 1")
	if err != nil {
		t.Fatal(err)
	}
	if got := status(t, root); got != before {
		t.Fatalf("Take changed git status:\n%s\nwas\n%s", got, before)
	}
	if ch, _ := r.Changed(ctx, s); ch {
		t.Fatal("fresh snapshot reports changes")
	}
	// "Shell" changes after the snapshot.
	write(t, filepath.Join(root, "a.txt"), "a3\n")
	write(t, filepath.Join(root, "sub/d.txt"), "d\n")
	write(t, filepath.Join(root, "c.txt"), "c back\n")
	os.Remove(filepath.Join(root, "b.txt"))
	write(t, filepath.Join(root, "ignored.log"), "changed\n")
	if ch, _ := r.Changed(ctx, s); !ch {
		t.Fatal("changes not detected")
	}
	done, err := r.Restore(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(done, "\n")
	for _, want := range []string{"restored " + filepath.Join(root, "a.txt"), "removed " + filepath.Join(root, "sub/d.txt"), "removed " + filepath.Join(root, "c.txt"), "restored " + filepath.Join(root, "b.txt")} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if read(filepath.Join(root, "a.txt")) != "a2\n" || read(filepath.Join(root, "b.txt")) != "b\n" {
		t.Fatalf("contents not restored: a=%q b=%q", read(filepath.Join(root, "a.txt")), read(filepath.Join(root, "b.txt")))
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("c.txt should be deleted again")
	}
	if _, err := os.Stat(filepath.Join(root, "sub/d.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("sub/d.txt should be removed")
	}
	if read(filepath.Join(root, "ignored.log")) != "changed\n" {
		t.Fatal("ignored files must be left alone")
	}
	if got := status(t, root); got != before {
		t.Fatalf("Restore touched the index or missed something:\n%s\nwant\n%s", got, before)
	}
	r.Drop(ctx, s)
	cmd := exec.Command("git", "for-each-ref", RefPrefix)
	cmd.Dir = root
	if out, _ := cmd.Output(); len(out) != 0 {
		t.Fatalf("ref not dropped: %s", out)
	}
	if _, err := Open(context.Background(), t.TempDir()); !errors.Is(err, ErrNotRepo) {
		t.Fatalf("non-repo err = %v", err)
	}
}

// A file the snapshot does not hold because it was ignored when it was
// taken is not a file created since. A turn that took a line out of
// .gitignore made the user's ignored file read as the turn's own, and
// the restore deleted it along with putting .gitignore back.
func TestRestoreKeepsFilesIgnoredWhenTheSnapshotWasTaken(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	r, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "ignored.log"), "precious\n")
	s, err := r.Take(ctx, "turn")
	if err != nil {
		t.Fatal(err)
	}
	// The turn: un-ignore the log, and create a file of its own.
	write(t, filepath.Join(root, ".gitignore"), "\n")
	write(t, filepath.Join(root, "made.txt"), "made\n")
	done, err := r.Restore(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(root, "ignored.log")) != "precious\n" {
		t.Fatalf("the ignored file was deleted: %q", done)
	}
	if read(filepath.Join(root, ".gitignore")) != "ignored.log\n" {
		t.Fatal(".gitignore not restored")
	}
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the turn's own file was not removed")
	}
}

func TestRestoreRefusesWhenHeadMoved(t *testing.T) {
	root := initRepo(t)
	ctx := context.Background()
	r, _ := Open(context.Background(), root)
	s, err := r.Take(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "a.txt"), "new\n")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "user commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	if _, err := r.Restore(ctx, s); !errors.Is(err, ErrHeadMoved) {
		t.Fatalf("err = %v", err)
	}
}

func TestTakeOnUnbornBranch(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	write(t, filepath.Join(root, "x.txt"), "x\n")
	r, _ := Open(context.Background(), root)
	s, err := r.Take(context.Background(), "first")
	if err != nil || s.Head != "" {
		t.Fatalf("snap=%+v err=%v", s, err)
	}
	os.Remove(filepath.Join(root, "x.txt"))
	if _, err := r.Restore(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if read(filepath.Join(root, "x.txt")) != "x\n" {
		t.Fatal("x.txt not restored")
	}
}

// A cancelled context is reported as such, not as a moved HEAD (which is
// what reading HEAD as "" used to suggest).
func TestRestoreWithCancelledContext(t *testing.T) {
	root := initRepo(t)
	r, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Take(context.Background(), "turn")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Restore(ctx, s); err == nil || errors.Is(err, ErrHeadMoved) {
		t.Fatalf("Restore with a cancelled ctx: %v", err)
	}
	if _, err := r.Take(ctx, "turn"); err == nil {
		t.Fatal("Take with a cancelled ctx succeeded")
	}
	if _, err := Open(ctx, root); err == nil {
		t.Fatal("Open with a cancelled ctx succeeded")
	}
}
