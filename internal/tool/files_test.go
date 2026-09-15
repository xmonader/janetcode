package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestReadFileWindow(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree\nfour\n"), 0o644)
	r := ReadFile{Root: root}
	out, err := r.Run(context.Background(), raw(`{"path":"a.txt","offset":2,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "2\ttwo\n3\tthree\n... (1 more lines)\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"missing.txt"}`)); err == nil {
		t.Fatal("expected error for missing file")
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"a.txt","bogus":1}`)); err == nil {
		t.Fatal("expected error for unknown field")
	}
	os.WriteFile(filepath.Join(root, "bin"), []byte("ab\x00cd"), 0o644)
	if _, err := r.Run(context.Background(), raw(`{"path":"bin"}`)); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error, got %v", err)
	}
}

func TestExtraDirAllowsRead(t *testing.T) {
	root := t.TempDir()
	extra := t.TempDir()
	if err := os.WriteFile(filepath.Join(extra, "x.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := NewLocal(root)
	if err := ws.AddDir(extra); err != nil {
		t.Fatal(err)
	}
	r := ReadFile{Root: root, WS: ws}
	out, err := r.Run(context.Background(), raw(`{"path":`+mustJSON(filepath.Join(extra, "x.txt"))+`}`))
	if err != nil || !strings.Contains(out, "hello") {
		t.Fatalf("extra read: %v %q", err, out)
	}
	if _, err := r.Run(context.Background(), raw(`{"path":"/etc/passwd"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside extra: %v", err)
	}
	bare := ReadFile{Root: root}
	if _, err := bare.Run(context.Background(), raw(`{"path":`+mustJSON(filepath.Join(extra, "x.txt"))+`}`)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("no extra dirs: %v", err)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestPathEscapeRejected(t *testing.T) {
	root := t.TempDir()
	r := ReadFile{Root: root}
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "a/../../x"} {
		if _, err := r.Run(context.Background(), raw(`{"path":"`+p+`"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
			t.Errorf("%s: expected outside-root error, got %v", p, err)
		}
	}
	// Symlink pointing outside root.
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))
	if _, err := r.Run(context.Background(), raw(`{"path":"link/secret"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("symlink: expected outside-root error, got %v", err)
	}
}

func TestWriteAndEdit(t *testing.T) {
	root := t.TempDir()
	w := WriteFile{Root: root}
	out, err := w.Run(context.Background(), raw(`{"path":"sub/dir/f.go","content":"package x\n\nfunc A() {}\nfunc B() {}\n"}`))
	if err != nil || out != "Created sub/dir/f.go (4 lines)" {
		t.Fatalf("write: %v %q", err, out)
	}
	e := EditFile{Root: root}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func Z","new_string":"func Y"}`)); err == nil {
		t.Fatal("expected not-found error")
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func ","new_string":"func "}`)); err == nil {
		t.Fatal("expected identical error")
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func ","new_string":"fn "}`)); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Fatalf("expected ambiguity error, got %v", err)
	}
	prev, err := e.Preview(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func A","new_string":"func Alpha"}`))
	if err != nil || !strings.Contains(prev, "-func A() {}\n+func Alpha() {}\n") {
		t.Fatalf("preview: %v\n%s", err, prev)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "sub/dir/f.go")); strings.Contains(string(got), "Alpha") {
		t.Fatal("preview must not modify the file")
	}
	out, err = e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"func A","new_string":"func Alpha"}`))
	if err != nil || !strings.HasPrefix(out, "Edited sub/dir/f.go (1 replacement, +1 -1 lines)") {
		t.Fatalf("edit: %v %q", err, out)
	}
	// The result shows the file around the change, so the model does not
	// have to read it back.
	if !strings.Contains(out, "     3\tfunc Alpha() {}") {
		t.Fatalf("edit result should quote the changed region: %q", out)
	}
	if _, err := e.Run(context.Background(), raw(`{"path":"sub/dir/f.go","old_string":"() {}","new_string":"() { return }","replace_all":true}`)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(root, "sub/dir/f.go"))
	want := "package x\n\nfunc Alpha() { return }\nfunc B() { return }\n"
	if string(got) != want {
		t.Fatalf("got %q", got)
	}
	// Overwrite preserves mode.
	os.Chmod(filepath.Join(root, "sub/dir/f.go"), 0o755)
	if prev, err := w.Preview(context.Background(), raw(`{"path":"sub/dir/f.go","content":"x"}`)); err != nil || !strings.Contains(prev, "-package x\n") || !strings.Contains(prev, "+x\n") {
		t.Fatalf("write preview: %v\n%s", err, prev)
	}
	if prev, err := w.Preview(context.Background(), raw(`{"path":"brand/new.txt","content":"a\nb\n"}`)); err != nil || prev != "--- brand/new.txt\n+++ brand/new.txt\n@@ -1,0 +1,2 @@\n+a\n+b\n" {
		t.Fatalf("new file preview: %v\n%q", err, prev)
	}
	if out, err := w.Run(context.Background(), raw(`{"path":"sub/dir/f.go","content":"x"}`)); err != nil || out != "Overwrote sub/dir/f.go (+1 -4 lines)" {
		t.Fatalf("overwrite: %v %q", err, out)
	}
	st, _ := os.Stat(filepath.Join(root, "sub/dir/f.go"))
	if st.Mode().Perm() != 0o755 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Join(root, "sub/dir"))
	if len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestListDir(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "d"), 0o755)
	os.WriteFile(filepath.Join(root, "b.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), nil, 0o644)
	l := ListDir{Root: root}
	out, err := l.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "a.txt\nb.txt\nd/" {
		t.Fatalf("got %q", out)
	}
	if out, _ := l.Run(context.Background(), raw(`{"path":"d"}`)); out != "(empty directory)" {
		t.Fatalf("got %q", out)
	}
}

func TestRegistryDefsSortedAndDuplicatePanics(t *testing.T) {
	r := NewRegistry(WriteFile{}, ReadFile{}, ListDir{})
	defs := r.Defs()
	if len(defs) != 3 || defs[0].Name != "list_dir" || defs[1].Name != "read_file" || defs[2].Name != "write_file" {
		t.Fatalf("defs = %+v", defs)
	}
	for _, d := range defs {
		if !json.Valid(d.InputSchema) {
			t.Errorf("%s: invalid schema", d.Name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate")
		}
	}()
	NewRegistry(ReadFile{}, ReadFile{})
}

// TestEditFileList checks that several edits to one file go in a single
// call, in order, and that a bad one leaves the file untouched.
func TestEditFileList(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	if err := os.WriteFile(path, []byte("package x\n\nfunc A() {}\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := EditFile{Root: root}
	in := raw(`{"path":"f.go","edits":[{"old_string":"func A","new_string":"func Alpha"},{"old_string":"func B","new_string":"func Beta"}]}`)
	out, err := e.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Edited f.go (2 replacements, +2 -2 lines)") {
		t.Fatalf("got %q", out)
	}
	got, _ := os.ReadFile(path)
	if want := "package x\n\nfunc Alpha() {}\nfunc Beta() {}\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The preview of a list is the diff of the whole batch.
	if err := os.WriteFile(path, []byte("package x\n\nfunc A() {}\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev, err := e.Preview(context.Background(), in)
	if err != nil || !strings.Contains(prev, "+func Alpha() {}") || !strings.Contains(prev, "+func Beta() {}") {
		t.Fatalf("preview: %v\n%s", err, prev)
	}
	// One bad edit fails the whole call and writes nothing.
	bad := raw(`{"path":"f.go","edits":[{"old_string":"func A","new_string":"func Alpha"},{"old_string":"missing","new_string":"x"}]}`)
	if _, err := e.Run(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "edit 2 of 2") {
		t.Fatalf("expected the second edit to be named: %v", err)
	}
	got, _ = os.ReadFile(path)
	if strings.Contains(string(got), "Alpha") {
		t.Fatalf("a failed batch must not write: %q", got)
	}
}

// TestEditFileByLine covers the cheaper locator: line ranges instead of
// retyping the text being replaced.
func TestEditFileByLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.go")
	orig := "package x\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\n"
	write := func() {
		if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write()
	e := EditFile{Root: root}

	out, err := e.Run(context.Background(), raw(`{"path":"f.go","from_line":3,"to_line":3,"new_string":"func Alpha() {}"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if want := "package x\n\nfunc Alpha() {}\nfunc B() {}\nfunc C() {}\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// The result quotes what the range actually held: a line number is a
	// claim nothing in the input checks.
	if !strings.Contains(out, "replaced line 3: func A() {}") {
		t.Fatalf("result must say what it replaced: %q", out)
	}

	// Several line edits in one call are all numbered against the file as
	// it was before the call, so the model need not track its own shifts.
	write()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.go","edits":[
{"from_line":3,"to_line":3,"new_string":"func Alpha() {}\nfunc Extra() {}"},
{"from_line":5,"to_line":5,"new_string":"func Gamma() {}"}]}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if want := "package x\n\nfunc Alpha() {}\nfunc Extra() {}\nfunc B() {}\nfunc Gamma() {}\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// Deleting a range.
	write()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.go","from_line":3,"to_line":4,"new_string":""}`)); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	if want := "package x\n\nfunc C() {}\n"; string(got) != want {
		t.Fatalf("delete: got %q want %q", got, want)
	}

	// Refusals, each leaving the file alone.
	write()
	for _, tc := range []struct{ in, want string }{
		{`{"path":"f.go","from_line":9,"to_line":9,"new_string":"x"}`, "past the end"},
		{`{"path":"f.go","from_line":4,"to_line":2,"new_string":"x"}`, "before from_line"},
		{`{"path":"f.go","from_line":0,"to_line":0,"old_string":"nothing here","new_string":"x"}`, "not found"},
		{`{"path":"f.go","from_line":3,"to_line":3,"old_string":"func A() {}","new_string":"x"}`, "not both"},
		{`{"path":"f.go","edits":[{"from_line":3,"to_line":4,"new_string":"x"},{"from_line":4,"to_line":5,"new_string":"y"}]}`, "both cover line 4"},
		{`{"path":"f.go","edits":[{"from_line":3,"to_line":3,"new_string":"x"},{"old_string":"func C","new_string":"y"}]}`, "locate itself the same way"},
	} {
		if _, err := e.Run(context.Background(), raw(tc.in)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("input %s: expected %q, got %v", tc.in, tc.want, err)
		}
	}
	if got, _ = os.ReadFile(path); string(got) != orig {
		t.Fatalf("a refused edit must not write: %q", got)
	}

	// A file with no trailing newline keeps not having one.
	noEnd := filepath.Join(root, "g.txt")
	os.WriteFile(noEnd, []byte("one\ntwo"), 0o644)
	if _, err := e.Run(context.Background(), raw(`{"path":"g.txt","from_line":1,"to_line":1,"new_string":"ONE"}`)); err != nil {
		t.Fatal(err)
	}
	if got, _ = os.ReadFile(noEnd); string(got) != "ONE\ntwo" {
		t.Fatalf("trailing newline changed: %q", got)
	}

	// The ablation withdraws the form entirely.
	write()
	if _, err := (EditFile{Root: root, NoLines: true}).Run(context.Background(),
		raw(`{"path":"f.go","from_line":3,"to_line":3,"new_string":"x"}`)); err == nil || !strings.Contains(err.Error(), "old_string only") {
		t.Fatalf("expected the line form to be withdrawn, got %v", err)
	}
	if d := (EditFile{Root: root, NoLines: true}).Description(); strings.Contains(d, "from_line") {
		t.Fatal("the withdrawn form must not be advertised")
	}
}

// FuzzEditFileLines checks the invariants of the line-range form against
// arbitrary input: it must never panic, never lose or invent a trailing
// newline, and never write a file it reported an error for. Line ranges
// are a new way to destroy the wrong part of a file, and --yolo runs them
// without a human looking at the diff.
func FuzzEditFileLines(f *testing.F) {
	f.Add("a\nb\nc\n", 1, 1, "X")
	f.Add("a\nb\nc", 2, 3, "")
	f.Add("", 1, 1, "x")
	f.Add("only\n", 1, 1, "one\ntwo\nthree")
	f.Add("a\nb\nc\n", 3, 1, "bad range")
	f.Fuzz(func(t *testing.T, body string, from, to int, repl string) {
		// Keep the inputs in a range where the interesting cases live.
		if len(body) > 4000 || len(repl) > 400 || from < -5 || from > 60 || to < -5 || to > 60 {
			t.Skip()
		}
		root := t.TempDir()
		path := filepath.Join(root, "f.txt")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Skip()
		}
		in, err := json.Marshal(map[string]any{
			"path": "f.txt", "from_line": from, "to_line": to, "new_string": repl,
		})
		if err != nil {
			t.Skip()
		}
		out, runErr := EditFile{Root: root}.Run(context.Background(), json.RawMessage(in))
		after, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("the file is gone after an edit: %v", readErr)
		}
		if runErr != nil {
			if string(after) != body {
				t.Fatalf("a refused edit wrote anyway:\nbefore %q\nafter  %q\nerr %v", body, after, runErr)
			}
			return
		}
		got := string(after)
		// A file that ended with a newline still does, and one that did
		// not still does not: silently changing it churns every diff.
		// A replacement that itself ends in a newline is asking for a
		// final empty line, so it is exempt.
		if !strings.HasSuffix(repl, "\n") && got != "" && body != "" &&
			strings.HasSuffix(body, "\n") != strings.HasSuffix(got, "\n") {
			t.Fatalf("trailing newline changed: before %q after %q repl %q", body, got, repl)
		}
		// The result must say which lines it replaced, or a mistargeted
		// range is invisible.
		if !strings.Contains(out, "replaced line") {
			t.Fatalf("no account of what was replaced: %q", out)
		}
	})
}

// TestEditFileDuplicateLineEdits: a real model sent the same line edit
// three times. Repeating a range with the same replacement is the model
// saying one thing twice, not a conflict, and refusing it costs a round
// trip to be told something unambiguous. Disagreeing edits still fail.
func TestEditFileDuplicateLineEdits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "v.go")
	orig := "package v\n\nfunc V(s string) bool {\n\treturn true\n}\n"
	os.WriteFile(path, []byte(orig), 0o644)
	e := EditFile{Root: root}

	in := raw(`{"path":"v.go","edits":[
{"from_line":4,"to_line":4,"new_string":"\treturn len(s) > 0"},
{"from_line":4,"to_line":4,"new_string":"\treturn len(s) > 0"},
{"from_line":4,"to_line":4,"new_string":"\treturn len(s) > 0"}]}`)
	if _, err := e.Run(context.Background(), in); err != nil {
		t.Fatalf("identical repeats should collapse: %v", err)
	}
	got, _ := os.ReadFile(path)
	if want := "package v\n\nfunc V(s string) bool {\n\treturn len(s) > 0\n}\n"; string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// Two edits to the same line that disagree cannot be resolved.
	os.WriteFile(path, []byte(orig), 0o644)
	bad := raw(`{"path":"v.go","edits":[
{"from_line":4,"to_line":4,"new_string":"\treturn a"},
{"from_line":4,"to_line":4,"new_string":"\treturn b"}]}`)
	if _, err := e.Run(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "different replacements") {
		t.Fatalf("disagreeing edits must still fail: %v", err)
	}
	if got, _ = os.ReadFile(path); string(got) != orig {
		t.Fatal("a refused batch must not write")
	}
	// Overlapping ranges that disagree are still refused.
	os.WriteFile(path, []byte(orig), 0o644)
	overlap := raw(`{"path":"v.go","edits":[
{"from_line":3,"to_line":4,"new_string":"x"},
{"from_line":4,"to_line":5,"new_string":"y"}]}`)
	if _, err := e.Run(context.Background(), overlap); err == nil {
		t.Fatal("overlapping ranges must still fail")
	}
}

// TestActionableErrors: an error the model cannot act on costs a whole
// round trip. Both of these came from watching a real model fail.
func TestActionableErrors(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "f.txt"), []byte("one\ntwo\n"), 0o644)

	// Reading a directory should name the tools that do the job.
	_, err := ReadFile{Root: root}.Run(context.Background(), raw(`{"path":"pkg"}`))
	if err == nil || !strings.Contains(err.Error(), "list_dir") {
		t.Fatalf("want a pointer to list_dir, got %v", err)
	}

	// A replacement with no location says so, rather than naming an
	// empty field the model may not have known it was sending.
	_, err = EditFile{Root: root}.Run(context.Background(), raw(`{"path":"f.txt","new_string":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "not where") {
		t.Fatalf("want a message about the missing location, got %v", err)
	}
	if !strings.Contains(err.Error(), "from_line") {
		t.Fatalf("the message should name both ways to locate an edit: %v", err)
	}
}

// TestEditFileRepairsMalformedEdits: both shapes were sent by real models.
// Refusing them costs a round trip to say something unambiguous.
func TestEditFileRepairsMalformedEdits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "f.txt")
	orig := "one\ntwo\nthree\n"
	e := EditFile{Root: root}
	reset := func() { os.WriteFile(path, []byte(orig), 0o644) }

	// The array double-encoded as a JSON string.
	reset()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.txt","edits":"[{\"old_string\":\"one\",\"new_string\":\"ONE\"}]"}`)); err != nil {
		t.Fatalf("a double-encoded edits array should be repaired: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "ONE\ntwo\nthree\n" {
		t.Fatalf("got %q", got)
	}

	// A single edit object where a list is expected.
	reset()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.txt","edits":{"old_string":"two","new_string":"TWO"}}`)); err != nil {
		t.Fatalf("a lone edit object should be wrapped: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\nTWO\nthree\n" {
		t.Fatalf("got %q", got)
	}

	// A single object, double-encoded, is both mistakes at once.
	reset()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.txt","edits":"{\"old_string\":\"three\",\"new_string\":\"THREE\"}"}`)); err != nil {
		t.Fatalf("both mistakes together should still be repaired: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "one\ntwo\nTHREE\n" {
		t.Fatalf("got %q", got)
	}

	// Nonsense is left alone so it fails with the normal error, not here.
	reset()
	if _, err := e.Run(context.Background(), raw(`{"path":"f.txt","edits":"not json at all"}`)); err == nil {
		t.Fatal("unrepairable input should still be refused")
	}
	if got, _ := os.ReadFile(path); string(got) != orig {
		t.Fatal("a refused edit must not write")
	}
}

// TestToolResultsNameFilesRelatively: a tool given an absolute path under
// the root reports the file by its relative name, which is what the user
// and the model read; a path outside the root is left as it is.
func TestToolResultsNameFilesRelatively(t *testing.T) {
	root := t.TempDir()
	if got := displayPath(root, filepath.Join(root, "pkg", "a.go")); got != filepath.Join("pkg", "a.go") {
		t.Fatalf("got %q", got)
	}
	if got := displayPath(root, "pkg/a.go"); got != "pkg/a.go" {
		t.Fatalf("relative stays: %q", got)
	}
	if got := displayPath(root, "/elsewhere/a.go"); got != "/elsewhere/a.go" {
		t.Fatalf("outside stays: %q", got)
	}
}
