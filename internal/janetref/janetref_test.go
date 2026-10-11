package janetref

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/gitx/gitxtest"
)

func janetrefRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitxtest.InitRepo(t, dir)
	janetrefGit(t, dir, "commit", "--allow-empty", "-q", "-m", "init")
	return dir
}

func janetrefGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A record is a commit on its ref with the files in its tree; the branch,
// HEAD and index are untouched; a write built on a stale tip is refused.
func TestWriteReadAndConflict(t *testing.T) {
	ctx := context.Background()
	repo := janetrefRepo(t)
	head := janetrefGit(t, repo, "rev-parse", "HEAD")
	ref := Prefix + "test"
	if tip, err := Tip(ctx, repo, ref); err != nil || tip != "" {
		t.Fatalf("Tip of a missing ref = %q, %v", tip, err)
	}
	first, err := Write(ctx, repo, ref, "", "one", map[string][]byte{"a.txt": []byte("A\n")})
	if err != nil {
		t.Fatal(err)
	}
	if data, ok, err := Read(ctx, repo, ref, "a.txt"); err != nil || !ok || string(data) != "A\n" {
		t.Fatalf("Read = %q %v %v", data, ok, err)
	}
	if _, ok, _ := Read(ctx, repo, ref, "missing"); ok {
		t.Fatal("a missing file was found")
	}
	if _, err := Write(ctx, repo, ref, "", "again", map[string][]byte{"a.txt": []byte("B\n")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a write that expected no ref = %v, want ErrConflict", err)
	}
	second, err := Write(ctx, repo, ref, first, "two", map[string][]byte{"a.txt": []byte("B\n")})
	if err != nil {
		t.Fatal(err)
	}
	if parent := janetrefGit(t, repo, "rev-parse", second+"^"); parent != first {
		t.Fatalf("parent = %s, want %s", parent, first)
	}
	if _, err := Write(ctx, repo, ref, first, "stale", map[string][]byte{"a.txt": []byte("C\n")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a write on a stale tip = %v, want ErrConflict", err)
	}
	if now := janetrefGit(t, repo, "rev-parse", "HEAD"); now != head {
		t.Fatal("HEAD moved")
	}
	if st := janetrefGit(t, repo, "status", "--porcelain"); st != "" {
		t.Fatalf("the work tree changed: %q", st)
	}
	if _, err := Write(ctx, repo, "refs/heads/main", "", "x", nil); err == nil {
		t.Fatal("a write outside refs/janetcode/ was allowed")
	}
	if _, err := Write(ctx, repo, ref, second, "x", map[string][]byte{"../a": nil}); err == nil {
		t.Fatal("a file name with a slash was allowed")
	}
}

func TestProbeMemoryAddDropAndCap(t *testing.T) {
	ctx := context.Background()
	repo := janetrefRepo(t)
	n, err := AddProbes(ctx, repo, []StoredProbe{{Cmd: "a", Why: "fix a"}, {Cmd: "b"}, {Cmd: "a"}, {Cmd: ""}})
	if err != nil || n != 2 {
		t.Fatalf("AddProbes = %d, %v", n, err)
	}
	tip1, _ := Tip(ctx, repo, ProbesRef)
	if n, err := AddProbes(ctx, repo, []StoredProbe{{Cmd: "a"}}); err != nil || n != 0 {
		t.Fatalf("re-adding = %d, %v", n, err)
	}
	if tip2, _ := Tip(ctx, repo, ProbesRef); tip2 != tip1 {
		t.Fatal("a write that changed nothing made a commit")
	}
	got, _, err := LoadProbes(ctx, repo)
	if err != nil || len(got) != 2 || got[0].Cmd != "a" || got[0].Why != "fix a" {
		t.Fatalf("LoadProbes = %+v, %v", got, err)
	}
	if n, err := DropProbes(ctx, repo, []string{"a", "zzz"}); err != nil || n != 1 {
		t.Fatalf("DropProbes = %d, %v", n, err)
	}
	var many []StoredProbe
	for i := range MaxProbes + 5 {
		many = append(many, StoredProbe{Cmd: strings.Repeat("x", i+1)})
	}
	if _, err := AddProbes(ctx, repo, many); err != nil {
		t.Fatal(err)
	}
	got, _, _ = LoadProbes(ctx, repo)
	if len(got) != MaxProbes || got[len(got)-1].Cmd != many[len(many)-1].Cmd {
		t.Fatalf("cap: %d probes, last %q", len(got), got[len(got)-1].Cmd)
	}
	for _, p := range got {
		if p.Cmd == "b" {
			t.Fatal("the oldest probe survived the cap")
		}
	}
}

func TestRelativize(t *testing.T) {
	for _, c := range []struct{ cmd, root, want string }{
		{`python3 /w/check.py`, "/w", `python3 ./check.py`},
		{`cd /w && python3 -c "import sys; sys.path.insert(0, '/w'); assert 1"`, "/w/", `cd . && python3 -c "import sys; sys.path.insert(0, '.'); assert 1"`},
		{`python3 /workspace/x.py`, "/work", `python3 /workspace/x.py`}, // a longer name is not the root
		{`python3 x.py`, "", `python3 x.py`},
	} {
		if got := Relativize(c.cmd, c.root); got != c.want {
			t.Errorf("Relativize(%q, %q) = %q, want %q", c.cmd, c.root, got, c.want)
		}
	}
}

func TestClipTranscriptKeepsEveryLineValid(t *testing.T) {
	long := strings.Repeat("é", 3000) // multi-byte: the clip must keep whole runes
	raw := []byte(`{"type":"meta","meta":{"id":"x","title":"t"}}` + "\n" +
		`{"type":"event","seq":1,"output":"` + long + `","n":12345678901234567890}` + "\n" +
		"not json " + strings.Repeat("y", 500) + "\n")
	out := ClipTranscript(raw, 100)
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, l := range lines[:2] {
		var v map[string]any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			t.Fatalf("line no longer JSON: %v: %s", err, l)
		}
	}
	if !strings.Contains(lines[1], "[clipped") || !strings.Contains(lines[1], "12345678901234567890") {
		t.Fatalf("clipped line = %s", lines[1])
	}
	if len(lines[2]) > 140 {
		t.Fatalf("a non-JSON line was not clipped: %d bytes", len(lines[2]))
	}
	// FitTranscript clips only as far as it must.
	if got, n, err := FitTranscript(raw, len(raw)); err != nil || n != 0 || !bytes.Equal(got, raw) {
		t.Fatalf("a transcript that fits was clipped: %d %v", n, err)
	}
	if _, n, err := FitTranscript(raw, 2000); err != nil || n == 0 {
		t.Fatalf("FitTranscript = %d, %v", n, err)
	}
	if _, _, err := FitTranscript(raw, 10); err == nil {
		t.Fatal("an impossible budget was met")
	}
}

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"20261010-024131-d921ad": true, "a": true,
		"": false, "../x": false, "a/b": false, "-x": false, "a..b": false, "a b": false,
	} {
		if ValidID(id) != want {
			t.Errorf("ValidID(%q) = %v", id, !want)
		}
	}
}

// A session saved, listed, opened into a sessions directory (never over
// one already there), pushed to a remote and fetched into another clone,
// with the probe memory merged rather than replaced.
func TestSessionRoundTripThroughARemote(t *testing.T) {
	ctx := context.Background()
	repo := janetrefRepo(t)
	bare := t.TempDir()
	gitxtest.InitBareRepo(t, bare)
	janetrefGit(t, repo, "remote", "add", "origin", bare)

	id := "20261010-024131-d921ad"
	transcript := `{"type":"meta","meta":{"id":"` + id + `","title":"fix paging","model":"m","start":{"head":"abc"}}}` + "\n" +
		`{"type":"event","seq":1,"t":"tool/result","output":"` + strings.Repeat("z", 4000) + `"}` + "\n"
	info, err := SaveSession(ctx, repo, id, SessionParts{Transcript: []byte(transcript), Evidence: []byte(`[]`), Probes: []StoredProbe{{Cmd: "p1", Session: id}}}, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if info.Clipped == 0 || info.Title != "fix paging" || info.Head != "abc" || info.RawBytes != len(transcript) {
		t.Fatalf("info = %+v", info)
	}
	if _, err := SaveSession(ctx, repo, "../evil", SessionParts{Transcript: []byte("{}")}, 0); err == nil {
		t.Fatal("a bad id was saved")
	}
	list, err := ListSessions(ctx, repo)
	if err != nil || len(list) != 1 || list[0].ID != id || list[0].Bytes == 0 || list[0].Bytes > 3000 {
		t.Fatalf("ListSessions = %+v, %v", list, err)
	}

	if _, err := AddProbes(ctx, repo, []StoredProbe{{Cmd: "mine"}}); err != nil {
		t.Fatal(err)
	}
	if refs, err := Push(ctx, repo, "", SessionsPrefix+"*", ProbesRef); err != nil || len(refs) != 2 {
		t.Fatalf("Push = %v, %v", refs, err)
	}
	if _, err := Push(ctx, repo, "--receive-pack=x", ProbesRef); err == nil {
		t.Fatal("a dash remote was used")
	}

	clone := filepath.Join(t.TempDir(), "clone")
	janetrefGit(t, filepath.Dir(clone), "clone", "-q", bare, clone)
	if refs, _ := Refs(ctx, clone, Prefix); len(refs) != 0 {
		t.Fatalf("a plain clone brought janetcode refs: %v", refs)
	}
	if _, err := AddProbes(ctx, clone, []StoredProbe{{Cmd: "theirs"}}); err != nil {
		t.Fatal(err)
	}
	if err := FetchSessions(ctx, clone, "", ""); err != nil {
		t.Fatal(err)
	}
	if n, err := FetchProbes(ctx, clone, ""); err != nil || n != 1 {
		t.Fatalf("FetchProbes = %d, %v", n, err)
	}
	probes, _, _ := LoadProbes(ctx, clone)
	if len(probes) != 2 || probes[0].Cmd != "theirs" || probes[1].Cmd != "mine" {
		t.Fatalf("merged probes = %+v", probes)
	}
	if refs, _ := Refs(ctx, clone, Prefix+"fetched/"); len(refs) != 0 {
		t.Fatalf("the fetch left a scratch ref: %v", refs)
	}

	dir := t.TempDir()
	path, err := OpenSession(ctx, clone, id, dir)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"title":"fix paging"`) || !strings.Contains(string(data), "[clipped") {
		t.Fatalf("opened transcript = %.200s", data)
	}
	os.WriteFile(path, []byte("local"), 0o600)
	if _, err := OpenSession(ctx, clone, id, dir); err == nil {
		t.Fatal("open overwrote a local session")
	}
	if got, _ := os.ReadFile(path); string(got) != "local" {
		t.Fatal("the local session changed")
	}
	if _, err := OpenSession(ctx, clone, "nope", dir); err == nil {
		t.Fatal("a missing session opened")
	}
}
