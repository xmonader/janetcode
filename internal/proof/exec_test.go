package proof

import (
	"context"
	"errors"
	"testing"
)

// A commit that is not there is said in words, and is ErrNoCommit so the
// CLI can give it the usage status: git's "fatal: not a git repository
// (or any parent up to mount point /)" and "exit status 1" are not shown.
func TestExecSaysWhyThereIsNoCommit(t *testing.T) {
	ctx := context.Background()
	empty := initRepo(t)
	withCommit := initRepo(t)
	write(t, withCommit, "a.txt", "a\n")
	commit(t, withCommit, "first")
	for _, c := range []struct{ dir, commit, want string }{
		{t.TempDir(), "", "not inside a git repository; proof notes live on commits"},
		{empty, "", "this repository has no commits yet; proof notes live on commits"},
		{withCommit, "nosuchref", "no commit named nosuchref in this repository"},
	} {
		_, err := Exec(ctx, c.dir, Args{Commit: c.commit}, nil)
		if !errors.Is(err, ErrNoCommit) || err.Error() != c.want {
			t.Errorf("%s %q: %v, want %q", c.dir, c.commit, err, c.want)
		}
	}
	if _, err := Exec(ctx, withCommit, Args{}, nil); err != nil {
		t.Fatalf("HEAD with no note: %v", err)
	}
}
