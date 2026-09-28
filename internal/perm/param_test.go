package perm

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestParseParamForms(t *testing.T) {
	for _, tc := range []struct {
		in     string
		tool   string
		params []Param
	}{
		{"task(model:opus)", "task", []Param{{"model", "opus"}}},
		{"task(agent:explore, model:haiku*)", "task", []Param{{"agent", "explore"}, {"model", "haiku*"}}},
		{"task( agent : explore ,model:haiku* )", "task", []Param{{"agent", "explore"}, {"model", "haiku*"}}},
		{"fetch_url(domain:example.com)", "fetch_url", []Param{{"domain", "example.com"}}},
		{"edit_file(path:internal/**)", "edit_file", []Param{{"path", "internal/**"}}},
		{"bash(input.command:go test *)", "bash", []Param{{"input.command", "go test *"}}},
	} {
		r, err := Parse(tc.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.in, err)
		}
		if r.Tool != tc.tool {
			t.Errorf("Parse(%q).Tool = %q", tc.in, r.Tool)
		}
		ps, ok, err := Params(r.Pattern)
		if err != nil || !ok {
			t.Fatalf("Params(%q) = %v %v %v", r.Pattern, ps, ok, err)
		}
		if len(ps) != len(tc.params) {
			t.Fatalf("Params(%q) = %v, want %v", r.Pattern, ps, tc.params)
		}
		for i := range ps {
			if ps[i] != tc.params[i] {
				t.Errorf("Params(%q)[%d] = %v, want %v", r.Pattern, i, ps[i], tc.params[i])
			}
		}
	}
	// Spacing is canonicalised, so the same rule written two ways is one
	// rule to Add, to the ledger and to the eye.
	r, err := Parse("task( agent:explore ,  model:haiku* )")
	if err != nil || r.String() != "task(agent:explore, model:haiku*)" {
		t.Fatalf("canonical form = %q (%v)", r.String(), err)
	}
}

// Patterns that are not the parameter form keep their old meaning, which
// is what makes every rule written before this keep working.
func TestParseSubjectFormsUnchanged(t *testing.T) {
	for _, in := range []string{
		"bash(npm run *)",
		"bash(go test *)",
		"read_file",
		"edit_file(internal/*)",
		"fetch_url(https://pkg.go.dev/*)",
		"fetch_url(http://localhost:8080/*)",
		"bash(git log --format=%h,%s)",
		"bash(make check)",
	} {
		r, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if r.String() != in {
			t.Errorf("Parse(%q).String() = %q", in, r.String())
		}
		if _, ok, err := Params(r.Pattern); ok || err != nil {
			t.Errorf("%q read as the parameter form (%v)", in, err)
		}
	}
}

func TestParseParamErrors(t *testing.T) {
	for _, bad := range []string{
		"task(model:)",
		"task(agent:explore, model)",
		"task(agent:explore,)",
		"task(agent:explore, agent:plan)",
		"task(agent:explore, :haiku)",
	} {
		if r, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted as %v", bad, r)
		}
	}
}

func call(tool, subject, input string) Call {
	return Call{Tool: tool, Subject: subject, Input: json.RawMessage(input)}
}

func TestParamMatching(t *testing.T) {
	for _, tc := range []struct {
		rule string
		c    Call
		want bool
	}{
		{"task(model:opus)", call("task", "", `{"prompt":"x","model":"opus"}`), true},
		{"task(model:opus)", call("task", "", `{"prompt":"x","model":"haiku"}`), false},
		{"task(model:haiku*)", call("task", "", `{"model":"haiku-4-5"}`), true},
		{"task(agent:explore, model:haiku*)", call("task", "", `{"agent":"explore","model":"haiku-4-5"}`), true},
		{"task(agent:explore, model:haiku*)", call("task", "", `{"agent":"plan","model":"haiku-4-5"}`), false},
		{"task(agent:explore, model:haiku*)", call("task", "", `{"agent":"explore"}`), false},
		// A dotted path reaches into a nested object.
		{"task(opts.model:opus)", call("task", "", `{"opts":{"model":"opus"}}`), true},
		{"task(opts.model:opus)", call("task", "", `{"opts":{"model":"sonnet"}}`), false},
		{"task(opts.model:opus)", call("task", "", `{"opts":"opus"}`), false},
		// Non-string fields are matched as they read.
		{"bash(timeout_ms:6000)", call("bash", "ls", `{"command":"ls","timeout_ms":6000}`), true},
		{"bash(background:true)", call("bash", "ls", `{"command":"ls","background":true}`), true},
		{"bash(background:true)", call("bash", "ls", `{"command":"ls"}`), false},
		// A list field matches when any element does.
		{"grep(globs:*.go)", call("grep", ".", `{"globs":["*.md","*.go"]}`), true},
		{"grep(globs:*.rs)", call("grep", ".", `{"globs":["*.md","*.go"]}`), false},
		// Each tool's own fields.
		{"bash(command:go test *)", call("bash", "go test ./...", `{"command":"go test ./..."}`), true},
		{"edit_file(path:internal/**)", call("edit_file", "internal/perm/perm.go", `{"path":"./internal/perm/perm.go"}`), true},
		{"edit_file(path:internal/**)", call("edit_file", "cmd/x.go", `{"path":"cmd/x.go"}`), false},
		{"write_file(path:*_test.go)", call("write_file", "a_test.go", `{"path":"a_test.go"}`), true},
		// fetch_url(domain:…) is the host, not the whole URL.
		{"fetch_url(domain:pkg.go.dev)", call("fetch_url", "https://pkg.go.dev/x", `{"url":"https://pkg.go.dev/x"}`), true},
		{"fetch_url(domain:*.example.com)", call("fetch_url", "", `{"url":"https://docs.example.com/a/b"}`), true},
		{"fetch_url(domain:example.com)", call("fetch_url", "", `{"url":"https://evil.test/example.com"}`), false},
		{"fetch_url(domain:example.com)", call("fetch_url", "", `{"url":"https://example.com:8443/x"}`), true},
		{"fetch_url(host:example.com:8443)", call("fetch_url", "", `{"url":"https://example.com:8443/x"}`), true},
		// The subject form still matches on the subject.
		{"bash(go test *)", call("bash", "go test ./...", `{"command":"go test ./..."}`), true},
		{"fetch_url(https://pkg.go.dev/*)", call("fetch_url", "https://pkg.go.dev/x", `{"url":"https://pkg.go.dev/x"}`), true},
	} {
		r, err := Parse(tc.rule)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.rule, err)
		}
		rs := Rules{Allow: []Rule{r}}
		got, _, _ := rs.DecideCall(tc.c)
		if (got == Allowed) != tc.want {
			t.Errorf("%s against %s: %v, want allowed=%v", tc.rule, tc.c.Input, got, tc.want)
		}
	}
}

// A parameter rule has nothing to read when the call's input was not
// passed, so it must not match — Decide (tool and subject only) is used
// by callers that never see the input.
func TestParamRuleNeedsInput(t *testing.T) {
	r, _ := Parse("task(model:opus)")
	rs := Rules{Allow: []Rule{r}}
	if v, _ := rs.Decide("task", ""); v != Ask {
		t.Fatalf("Decide without input = %v, want ask", v)
	}
	if v, _, _ := rs.DecideCall(call("task", "", `{"model":"opus"}`)); v != Allowed {
		t.Fatalf("DecideCall = %v, want allow", v)
	}
}

// The ledger keys on the rule string, so a parameter rule needs nothing
// from it beyond parsing back to itself.
func TestLedgerKeysParamRules(t *testing.T) {
	l := OpenLedger(t.TempDir(), "proj")
	shape := "task(agent:explore, model:haiku*)"
	for range LearnAfter {
		l.Record(shape, true)
	}
	if got := l.Get(shape).Allowed; got != LearnAfter {
		t.Fatalf("tally = %d", got)
	}
	lines := CandidateLines(l, Rules{})
	if len(lines) != 1 || lines[0] != "/permissions allow "+shape+" (approved 3×, never refused)" {
		t.Fatalf("candidate lines = %v", lines)
	}
	r, _ := Parse(shape)
	if CandidateLines(l, Rules{Allow: []Rule{r}}) != nil {
		t.Fatal("already-saved parameter rule still offered")
	}
}

// The file tools accept an absolute path inside the workspace as readily
// as a relative one, so a rule written the way the repository spells a
// path has to hold for both spellings — above all a deny rule, which an
// absolute path otherwise walked straight past.
func TestFileRulesHoldForEitherSpelling(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "proj")
	rules, err := File{
		Deny:  []string{"edit_file(secrets/*)", "write_file(path:secrets/*)"},
		Allow: []string{"read_file(" + filepath.Join(root, "docs") + "/*)"},
	}.Rules()
	if err != nil {
		t.Fatal(err)
	}
	call := func(tool, path string) Call {
		in, _ := json.Marshal(map[string]string{"path": path})
		return Call{Tool: tool, Subject: SubjectIn(tool, in, root), Input: in, Root: root}
	}
	for _, c := range []Call{
		call("edit_file", filepath.Join(root, "secrets", "key")),
		call("edit_file", "secrets/key"),
		call("write_file", filepath.Join(root, "secrets", "key")),
		call("write_file", "./secrets/key"),
	} {
		if v, _, _ := rules.DecideCall(c); v != Denied {
			t.Errorf("%s %s = %v, want deny", c.Tool, c.Subject, v)
		}
	}
	if v, _, _ := rules.DecideCall(call("read_file", "docs/a.md")); v != Allowed {
		t.Errorf("an absolute allow rule did not cover the relative spelling: %v", v)
	}
	// Outside the workspace there is no second spelling.
	other := filepath.Join(string(filepath.Separator), "other", "secrets", "key")
	if v, _, _ := rules.DecideCall(call("edit_file", other)); v != Ask {
		t.Errorf("a path outside the workspace = %v, want ask", v)
	}
}

// TestDriveLetterIsAPath: a Windows path in a rule names a file, not a
// field called "C".
func TestDriveLetterIsAPath(t *testing.T) {
	for _, in := range []string{`read_file(C:/Users/me/x.go)`, `read_file(D:\src\x.go)`} {
		r, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", in, err)
		}
		ps, isParam, perr := Params(r.Pattern)
		if perr != nil || isParam {
			t.Fatalf("Parse(%q) read parameters %+v (err %v); want a path pattern", in, ps, perr)
		}
	}
}
