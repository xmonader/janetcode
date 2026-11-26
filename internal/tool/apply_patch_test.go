package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func patchIn(p string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"input": p})
	return b
}

func runPatch(t *testing.T, root, p string) (string, error) {
	t.Helper()
	return ApplyPatch{Root: root, Seen: &Seen{}}.Run(context.Background(), patchIn(p))
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePatchAllOps(t *testing.T) {
	ops, err := parsePatch(`noise before
*** Begin Patch
*** Add File: a.txt
+one
two
*** Delete File: b.txt
*** Update File: c.txt
*** Move to: d.txt
@@ func f
 ctx
-old
+new
*** End of File
*** End Patch
trailing`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 3 || ops[0].kind != "add" || ops[1].kind != "delete" || ops[2].kind != "update" {
		t.Fatalf("%+v", ops)
	}
	if strings.Join(ops[0].lines, "|") != "one|two" {
		t.Fatalf("add lines %v", ops[0].lines)
	}
	u := ops[2]
	if u.move != "d.txt" || len(u.hunks) != 1 || u.hunks[0].context != "func f" || !u.hunks[0].eof || len(u.hunks[0].lines) != 3 {
		t.Fatalf("%+v", u)
	}
}

func TestParsePatchTruncated(t *testing.T) {
	_, err := parsePatch("*** Begin Patch\n*** Update File: a\n@@\n-x\n+y\n")
	if err == nil || !strings.Contains(err.Error(), "cut off by the output-token limit") {
		t.Fatalf("%v", err)
	}
}

func TestParsePatchRefusesElision(t *testing.T) {
	for _, l := range []string{"// ... existing code ...", "# ... rest unchanged", "...", "/* rest of the file */"} {
		_, err := parsePatch("*** Begin Patch\n*** Add File: a\n+" + l + "\n*** End Patch")
		if err == nil || !strings.Contains(err.Error(), "placeholder") {
			t.Errorf("%q: %v", l, err)
		}
	}
	if _, err := parsePatch("*** Begin Patch\n*** Add File: a\n+x := f(...)\n*** End Patch"); err != nil {
		t.Fatalf("real code refused: %v", err)
	}
}

func TestApplyPatchEndToEnd(t *testing.T) {
	root := t.TempDir()
	write(t, root, "c.txt", "a\nb\nc\n")
	write(t, root, "gone.txt", "bye\n")
	out, err := runPatch(t, root, `*** Begin Patch
*** Add File: new/n.txt
+hello
*** Delete File: gone.txt
*** Delete File: missing.txt
*** Update File: c.txt
*** Move to: moved.txt
@@
 a
-b
+B
*** End Patch`)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, root, "new/n.txt") != "hello\n" || read(t, root, "moved.txt") != "a\nB\nc\n" {
		t.Fatal("content wrong")
	}
	for _, n := range []string{"gone.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(root, n)); err == nil {
			t.Fatalf("%s should be gone", n)
		}
	}
	if !strings.Contains(out, "missing.txt does not exist; nothing to delete") {
		t.Fatalf("no note: %s", out)
	}
}

func TestApplyPatchUpdateMissingFile(t *testing.T) {
	_, err := runPatch(t, t.TempDir(), "*** Begin Patch\n*** Update File: nope\n@@\n-a\n+b\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("%v", err)
	}
}

func TestApplyPatchFuzzyRungKeepsFileContext(t *testing.T) {
	root := t.TempDir()
	// The file has an em dash and trailing spaces; the patch quotes a hyphen.
	write(t, root, "f.txt", "keep — this  \nold\nend\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: f.txt\n@@\n keep - this\n-old\n+new\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "f.txt"); got != "keep — this  \nnew\nend\n" {
		t.Fatalf("%q", got)
	}
}

func TestApplyPatchLeadingWhitespaceNeverRelaxed(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.py", "if x:\n    y = 1\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: f.py\n@@\n-  y = 1\n+  y = 2\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "hunk 1 in f.py did not match") {
		t.Fatalf("%v", err)
	}
	if read(t, root, "f.py") != "if x:\n    y = 1\n" {
		t.Fatal("file changed")
	}
}

func TestApplyPatchAmbiguous(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", "x\nx\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: f.txt\n@@\n-x\n+y\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "more unique context") {
		t.Fatalf("%v", err)
	}
}

func TestApplyPatchForwardCursor(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", "x\nmid\nx\n")
	// Hunk 1 consumes the first x; hunk 2's x is then unique going forward.
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: f.txt\n@@\n-x\n+first\n mid\n@@\n-x\n+last\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "f.txt"); got != "first\nmid\nlast\n" {
		t.Fatalf("%q", got)
	}
	// Out of order hunks do not match backwards.
	write(t, root, "g.txt", "a\nb\n")
	_, err = runPatch(t, root, "*** Begin Patch\n*** Update File: g.txt\n@@\n-b\n+B\n@@\n-a\n+A\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "hunk 2 in g.txt did not match") {
		t.Fatalf("%v", err)
	}
}

func TestApplyPatchMissHint(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.go", "func compute(a int) int {\n\treturn a\n}\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: f.go\n@@\n-func compute(b int) int {\n+func compute(c int) int {\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "did not match") || !strings.Contains(err.Error(), "compute") {
		t.Fatalf("%v", err)
	}
}

func TestApplyPatchAtomicRollback(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "1\n")
	write(t, root, "blocker", "i am a file\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: a.txt\n@@\n-1\n+2\n*** Add File: blocker/x.txt\n+x\n*** End Patch")
	if err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("%v", err)
	}
	if read(t, root, "a.txt") != "1\n" {
		t.Fatal("a.txt not rolled back")
	}
}

func TestApplyPatchNothingWrittenOnMissedHunk(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "1\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Add File: z.txt\n+z\n*** Update File: a.txt\n@@\n-nope\n+x\n*** End Patch")
	if err == nil {
		t.Fatal("want error")
	}
	if _, err := os.Stat(filepath.Join(root, "z.txt")); err == nil {
		t.Fatal("z.txt written before the failure was known")
	}
}

func TestApplyPatchCRLF(t *testing.T) {
	root := t.TempDir()
	write(t, root, "w.txt", "a\r\nb\r\n")
	write(t, root, "m.txt", "a\r\nb\n")
	_, err := runPatch(t, root, "*** Begin Patch\n*** Update File: w.txt\n@@\n-b\n+B\n+C\n*** Update File: m.txt\n@@\n-b\n+B\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "w.txt"); got != "a\r\nB\r\nC\r\n" {
		t.Fatalf("%q", got)
	}
	if got := read(t, root, "m.txt"); got != "a\r\nB\n" {
		t.Fatalf("mixed file: %q", got)
	}
}

func TestApplyPatchChangedSinceRead(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.txt", "1\n")
	seen := &Seen{}
	seen.Record(filepath.Join(root, "a.txt"), []byte("1\n"))
	write(t, root, "a.txt", "1\nextra\n")
	_, err := ApplyPatch{Root: root, Seen: seen}.Run(context.Background(), patchIn("*** Begin Patch\n*** Delete File: a.txt\n*** End Patch"))
	if err == nil {
		t.Fatal("delete of a changed file allowed")
	}
	if read(t, root, "a.txt") != "1\nextra\n" {
		t.Fatal("file touched")
	}
}

func TestApplyPatchConfined(t *testing.T) {
	root := t.TempDir()
	_, err := runPatch(t, root, "*** Begin Patch\n*** Add File: ../escape.txt\n+x\n*** End Patch")
	if err == nil {
		t.Fatal("escaped the workspace")
	}
}

func TestApplyPatchTouchesEveryFile(t *testing.T) {
	var _ Toucher = ApplyPatch{}
	var _ Previewer = ApplyPatch{}
	var _ Guard = ApplyPatch{}
	root := t.TempDir()
	ps, err := ApplyPatch{Root: root}.Touches(patchIn("*** Begin Patch\n*** Add File: a\n+x\n*** Update File: b\n*** Move to: c\n*** End Patch"))
	if err != nil || len(ps) != 3 {
		t.Fatalf("%v %v", ps, err)
	}
}

func TestPatchModel(t *testing.T) {
	for id, want := range map[string]bool{
		"gpt-5": true, "gpt-4.1-mini": true, "openai/gpt-5-codex": true, "o3": true, "o4-mini": true,
		"codex-mini-latest": true, "GPT-5": true,
		"claude-sonnet-5-5": false, "kimi-k2": false, "gemini-2.5-pro": false, "o1": false, "": false,
	} {
		if PatchModel(id) != want {
			t.Errorf("PatchModel(%q) != %v", id, want)
		}
	}
	if !PatchModel("my-tuned-model", "my-tuned-*") {
		t.Error("configured glob ignored")
	}
}

func TestApplyPatchAcceptsPatchArgument(t *testing.T) {
	root := t.TempDir()
	b, _ := json.Marshal(map[string]string{"patch": "*** Begin Patch\n*** Add File: p.txt\n+hi\n*** End Patch"})
	if _, err := (ApplyPatch{Root: root, Seen: &Seen{}}).Run(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := read(t, root, "p.txt"); got != "hi\n" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyPatchNeitherArgumentErrors(t *testing.T) {
	for _, in := range []string{`{}`, `{"input":"","patch":"  "}`} {
		_, err := ApplyPatch{Root: t.TempDir(), Seen: &Seen{}}.Run(context.Background(), json.RawMessage(in))
		if err == nil || !strings.Contains(err.Error(), "`input`") {
			t.Fatalf("%s: err = %v", in, err)
		}
	}
}
