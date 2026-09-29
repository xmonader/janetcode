package perm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseMatchDecide(t *testing.T) {
	for _, bad := range []string{"", "bash(", "bash go", "(x)"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	r, err := Parse(" bash( go test  * ) ")
	if err != nil || r.Tool != "bash" || r.Pattern != "go test  *" || r.String() != "bash(go test  *)" {
		t.Fatalf("parse = %+v %v", r, err)
	}
	rs := Rules{}
	for _, s := range []string{"bash(go test *)", "bash(make *)", "read_file", "edit_file(internal/*)", "fetch_url(https://pkg.go.dev/*)"} {
		r, _ := Parse(s)
		rs.Add(Allowed, r)
	}
	d, _ := Parse("bash(git push*)")
	rs.Add(Denied, d)
	rs.Add(Denied, d)
	if len(rs.Deny) != 1 {
		t.Fatal("duplicate deny added")
	}
	cases := []struct {
		tool, subject string
		want          Verdict
	}{
		{"bash", "go test ./...", Allowed},
		{"bash", "go  test   ./internal/...", Allowed},
		{"bash", "go build ./...", Ask},
		{"bash", "make test", Allowed},
		{"bash", "git push origin main", Denied},
		{"bash", "git push", Denied},
		{"read_file", "anything/at/all", Allowed},
		{"edit_file", "internal/agent/x.go", Allowed},
		{"edit_file", "cmd/x.go", Ask},
		{"write_file", "internal/x.go", Ask},
		{"fetch_url", "https://pkg.go.dev/net/http", Allowed},
		{"fetch_url", "https://evil.example/", Ask},
	}
	for _, c := range cases {
		if got, _ := rs.Decide(c.tool, c.subject); got != c.want {
			t.Errorf("%s %q = %v, want %v", c.tool, c.subject, got, c.want)
		}
	}
	// Deny beats allow even when both match.
	rs.Add(Allowed, Rule{Tool: "bash"})
	if got, r := rs.Decide("bash", "git push origin"); got != Denied || r.String() != "bash(git push*)" {
		t.Fatalf("deny precedence: %v %v", got, r)
	}
	if !Match("a?c", "abc") || Match("a?c", "ac") || !Match("*", "") || Match("abc", "abcd") {
		t.Fatal("glob edge cases")
	}
	for _, c := range []struct {
		pattern, subject string
		want             bool
	}{
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxbyy", false},
		{"**", "anything at all", true},
		{"*x", "x", true},
		{"*x", "", false},
		{"a*", "a", true},
		{"*a*a*a*", "aaa", true},
	} {
		if got := Match(c.pattern, c.subject); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.subject, got, c.want)
		}
	}
}

// The matcher is run against a subject the model chose. A pattern with
// several stars used to backtrack exponentially, so a saved rule plus a
// long command was enough to wedge the permission check; this finishes
// in milliseconds and would not finish at all on the old matcher.
func TestMatchDoesNotBacktrackExponentially(t *testing.T) {
	pattern := "go test *a*a*a*a*a*a*a*a*a*a*a*a*b"
	subject := "go test " + strings.Repeat("a", 4000)
	done := make(chan bool, 1)
	go func() { done <- Match(pattern, subject) }()
	select {
	case got := <-done:
		if got {
			t.Fatal("matched a subject with no trailing b")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the glob matcher did not finish")
	}
}

func TestSubjectAndSuggest(t *testing.T) {
	if s := Subject("bash", json.RawMessage(`{"command":"  go test ./... "}`)); s != "go test ./..." {
		t.Fatalf("bash subject %q", s)
	}
	if s := Subject("edit_file", json.RawMessage(`{"path":"./internal/../internal/x.go"}`)); s != "internal/x.go" {
		t.Fatalf("path subject %q", s)
	}
	if s := Subject("grep", json.RawMessage(`{"pattern":"x"}`)); s != "." {
		t.Fatalf("grep subject %q", s)
	}
	if s := Subject("mcp__x__y", json.RawMessage(`{"query":"q"}`)); s != "q" {
		t.Fatalf("mcp subject %q", s)
	}
	for _, c := range []struct{ tool, subject, want string }{
		{"bash", "go test ./...", "bash(go test *)"},
		{"bash", "go test ./... 2>&1", "bash(go test *)"},
		{"bash", "make", "bash(make)"},
		{"bash", "ls -la /tmp", "bash(ls *)"},
		{"bash", "make build", "bash(make *)"},
		{"bash", "python3 -m pytest", "bash(python3 *)"},
		{"bash", "git commit -m x", "bash(git commit *)"},
		{"bash", "./scripts/run.sh --all", "bash(./scripts/run.sh *)"},
		{"fetch_url", "https://pkg.go.dev/net/http?tab=doc", "fetch_url(https://pkg.go.dev/*)"},
		{"edit_file", "internal/agent/x.go", "edit_file(internal/agent/*)"},
		{"notebook_edit", "analysis/q3.ipynb", "notebook_edit(analysis/*)"},
		{"write_file", "x.go", "write_file"},
		{"mcp__x__y", "q", "mcp__x__y"},
	} {
		if got := Suggest(c.tool, c.subject).String(); got != c.want {
			t.Errorf("Suggest(%s, %q) = %s, want %s", c.tool, c.subject, got, c.want)
		}
	}
}

func TestLoadProjectAndMerge(t *testing.T) {
	root := t.TempDir()
	if rs, h, err := LoadProject(root); len(rs.Allow) != 0 || h != "" || err != nil {
		t.Fatal("missing file should be empty")
	}
	os.MkdirAll(filepath.Join(root, ".janetcode"), 0o755)
	os.WriteFile(filepath.Join(root, ProjectFile), []byte(`{"allow":["bash(make *)"],"deny":["bash(rm *)"]}`), 0o644)
	rs, h, err := LoadProject(root)
	if err != nil || len(h) != 64 || len(rs.Allow) != 1 || len(rs.Deny) != 1 {
		t.Fatalf("%+v %q %v", rs, h, err)
	}
	os.WriteFile(filepath.Join(root, ProjectFile), []byte(`{"allow":["bash("]}`), 0o644)
	if _, _, err := LoadProject(root); err == nil {
		t.Fatal("bad rule accepted")
	}
	m := Merge(Rules{Allow: []Rule{{Tool: "read_file"}}}, rs)
	if len(m.Allow) != 2 || len(m.Deny) != 1 || m.Allow[0].Tool != "read_file" {
		t.Fatalf("merge = %+v", m)
	}
}

// The rule shapes that grant or refuse the broadest access, and that no
// test held down: a rule on every tool, and a rule with an explicit but
// empty pattern, which matches every call of its tool.
func TestWildcardToolAndEmptyPattern(t *testing.T) {
	star, err := Parse("*(*secret*)")
	if err != nil {
		t.Fatal(err)
	}
	if star.Tool != "*" || star.Pattern != "*secret*" {
		t.Fatalf("parsed %+v", star)
	}
	rs := Rules{Deny: []Rule{star}}
	// A deny on * has to reach every tool, whatever it is called.
	for _, tool := range []string{"bash", "read_file", "edit_file", "mcp__x__y", "something_new"} {
		if v, _ := rs.Decide(tool, "cat /etc/secrets/db"); v != Denied {
			t.Fatalf("%s was not denied by a rule on every tool", tool)
		}
	}
	// And only where the pattern matches.
	if v, _ := rs.Decide("bash", "ls"); v != Ask {
		t.Fatalf("an unrelated subject was decided %q", v)
	}

	// tool() is an explicit empty pattern, which means "every call".
	empty, err := Parse("read_file()")
	if err != nil {
		t.Fatal(err)
	}
	if empty.Pattern != "" {
		t.Fatalf("parsed %+v", empty)
	}
	allow := Rules{Allow: []Rule{empty}}
	for _, subject := range []string{"a.go", "/etc/passwd", ""} {
		if v, _ := allow.Decide("read_file", subject); v != Allowed {
			t.Fatalf("read_file(%q) was decided %q", subject, v)
		}
	}
	if v, _ := allow.Decide("bash", "ls"); v != Ask {
		t.Fatal("an empty pattern must not widen past its own tool")
	}

	// A bare rule on everything, with no pattern at all, stops the lot.
	if v, _ := (Rules{Deny: []Rule{{Tool: "*"}}}).Decide("bash", "ls"); v != Denied {
		t.Fatal("a bare deny on * let a call through")
	}
}

// Add refuses duplicates on the allow list too, not only on deny: a rule
// saved twice from two "always allow" answers would otherwise grow the
// file without bound and show twice in /permissions.
func TestAddDeduplicatesAllowAndDeny(t *testing.T) {
	var rs Rules
	r := Rule{Tool: "bash", Pattern: "go test *"}
	if !rs.Add(Allowed, r) {
		t.Fatal("the first add should report a change")
	}
	if rs.Add(Allowed, r) {
		t.Fatal("the same allow rule was added twice")
	}
	if len(rs.Allow) != 1 {
		t.Fatalf("allow = %+v", rs.Allow)
	}
	// The same rule on the other list is a different thing.
	if !rs.Add(Denied, r) {
		t.Fatal("deny should be tracked separately from allow")
	}
	if rs.Add(Denied, r) {
		t.Fatal("the same deny rule was added twice")
	}
}

// A saved rule's * spans everything that follows, && and ; included, so
// Suggest must never offer one for a command whose leading words are not
// the command that decides what runs. A model that prefixes every call
// with `cd <workspace> &&` had `bash(cd *)` offered, which allows anything.
func TestSuggestNeverCapturesCompoundOrWrapperCommands(t *testing.T) {
	for _, subject := range []string{
		"cd x && rm -rf y",
		"cd /work/repo && go test ./... && rm -rf ~",
		"go test ./... ; rm -rf y",
		"cat a.txt | sh",
		"echo $(rm -rf y)",
		"echo `rm -rf y`",
		"cd internal",
		"sudo go test ./...",
		"env FOO=1 go test ./...",
		"sh -c 'go test ./...'",
		"bash -c 'rm -rf y'",
		"xargs rm",
		"eval go test",
		"export X=1",
		"time go test ./...",
		"nohup ./server",
		// An assignment prefix is env without the word: FOO=1 * allows
		// any command at all with FOO=1 in front of it.
		"FOO=1 go test ./...",
		"CGO_ENABLED=0 go build",
		// A wrapper named by its path is still the wrapper.
		"/usr/bin/sudo go test ./...",
		"/bin/sh -c 'rm -rf y'",
	} {
		r := Suggest("bash", subject)
		if !r.IsZero() {
			t.Errorf("Suggest(bash, %q) = %s, want no rule", subject, r)
		}
	}
	if r := Suggest("bash", "cd x && rm -rf y"); r.String() == "bash(go test *)" || Match("go test *", "cd x && rm -rf y") {
		t.Fatalf("bash(go test *) must not cover cd x && rm -rf y: %s", r)
	}
	// A simple command still gets its rule.
	if r := Suggest("bash", "go test ./..."); r.String() != "bash(go test *)" {
		t.Fatalf("simple command = %s", r)
	}
	// And * really does span operators, which is why the above matters.
	if !Match("go test *", "go test ./... && rm -rf y") {
		t.Fatal("Match documents * as spanning everything")
	}
}

// A deny rule on bash refuses the command wherever it appears in a
// compound line, not only when it leads: bash(rm *) was walked past by
// `ls && rm -rf src`, since the rule is matched as a prefix.
func TestBashDenyRuleMatchesAnySegment(t *testing.T) {
	rules, err := File{Deny: []string{"bash(rm *)", "bash(git push*)"}, Allow: []string{"bash(ls *)"}}.Rules()
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"rm -rf src",
		"ls && rm -rf src",
		"ls; rm -rf src",
		"true || rm -rf src",
		"ls | rm -rf src",
		"ls\nrm -rf src",
		"go test ./... && git push --force",
		"(cd x && rm -rf y)",
	} {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		if v, _, _ := rules.DecideCall(Call{Tool: "bash", Subject: cmd, Input: in}); v != Denied {
			t.Errorf("%q = %v, want deny", cmd, v)
		}
	}
	for _, cmd := range []string{"ls -la", "echo 'a && rm -rf b'", "ls && echo rm"} {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		if v, _, _ := rules.DecideCall(Call{Tool: "bash", Subject: cmd, Input: in}); v == Denied {
			t.Errorf("%q denied", cmd)
		}
	}
}

// The subject a rule is matched against drops a leading `cd <workspace>
// &&`, so the rule saved for `go test` matches the model's next
// `cd <workspace> && go test` too, and the suggestion is for `go test`.
func TestSubjectInDropsWorkspaceCd(t *testing.T) {
	root := t.TempDir()
	in, _ := json.Marshal(map[string]string{"command": "cd " + root + " && go test ./internal/x"})
	s := SubjectIn("bash", in, root)
	if s != "go test ./internal/x" {
		t.Fatalf("subject = %q", s)
	}
	if r := Suggest("bash", s); r.String() != "bash(go test *)" {
		t.Fatalf("suggest = %s", r)
	}
	other, _ := json.Marshal(map[string]string{"command": "cd /elsewhere && go test ./x"})
	if s := SubjectIn("bash", other, root); s != "cd /elsewhere && go test ./x" {
		t.Fatalf("a cd elsewhere is part of the command: %q", s)
	}
}

// TestSuggestLadder: every call that gets a rule gets a ladder of them,
// narrowest first, with Suggest's answer somewhere in it.
func TestSuggestLadder(t *testing.T) {
	for _, c := range []struct {
		tool, subject string
		want          []string
		def           int
	}{
		{"bash", "go test ./internal/...", []string{"bash(go test ./internal/...)", "bash(go test *)", "bash(go *)"}, 1},
		{"bash", "go build", []string{"bash(go build)", "bash(go build *)", "bash(go *)"}, 1},
		{"bash", "go -h", []string{"bash(go -h)", "bash(go *)"}, 1},
		{"bash", "make", []string{"bash(make)"}, 0},
		{"bash", "ls -la /tmp", []string{"bash(ls -la /tmp)", "bash(ls *)"}, 1},
		{"bash", "git commit -m x", []string{"bash(git commit -m x)", "bash(git commit *)", "bash(git *)"}, 1},
		{"bash", "go test ./... 2>&1", []string{"bash(go test ./... 2>&1)", "bash(go test *)", "bash(go *)"}, 1},
		{"edit_file", "internal/agent/x.go", []string{"edit_file(internal/agent/x.go)", "edit_file(internal/agent/*)", "edit_file(internal/*)"}, 1},
		{"read_file", "/etc/hosts", []string{"read_file(/etc/hosts)", "read_file(/etc/*)"}, 1},
		{"write_file", "x.go", []string{"write_file(x.go)", "write_file"}, 1},
		{"grep", ".", []string{"grep"}, 0},
		{"fetch_url", "https://pkg.go.dev/net/http?tab=doc", []string{"fetch_url(https://pkg.go.dev/net/http?tab=doc)", "fetch_url(https://pkg.go.dev/net/*)", "fetch_url(https://pkg.go.dev/*)"}, 2},
		{"fetch_url", "https://example.com", []string{"fetch_url(https://example.com)", "fetch_url(https://example.com/*)"}, 1},
		{"mcp__x__y", "q", []string{"mcp__x__y"}, 0},
	} {
		rungs := SuggestLadder(c.tool, c.subject)
		var got []string
		for _, r := range rungs {
			got = append(got, r.String())
		}
		if len(got) != len(c.want) {
			t.Errorf("ladder(%s, %q) = %v, want %v", c.tool, c.subject, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("ladder(%s, %q) = %v, want %v", c.tool, c.subject, got, c.want)
				break
			}
		}
		if d := DefaultRung(rungs, Suggest(c.tool, c.subject)); d != c.def {
			t.Errorf("default rung for (%s, %q) = %d (%v), want %d", c.tool, c.subject, d, got, c.def)
		}
	}
}

// TestSuggestLadderFloor: the ladder never reaches a rule broader than
// the tool's own name, never offers one at all for a command Suggest
// refuses, and every rung it does offer matches the call it came from.
func TestSuggestLadderFloor(t *testing.T) {
	for _, subject := range []string{"cd /tmp && rm -rf x", "sudo rm -rf /", "sh -c 'rm x'", "ls | xargs rm", "echo `id`", "echo $(id)", "cd /tmp"} {
		if rungs := SuggestLadder("bash", subject); rungs != nil {
			t.Errorf("SuggestLadder(bash, %q) = %v, want no ladder", subject, rungs)
		}
		if r := Suggest("bash", subject); !r.IsZero() {
			t.Errorf("Suggest(bash, %q) = %s, want no rule", subject, r)
		}
	}
	for _, c := range []struct{ tool, subject string }{
		{"bash", "go test ./internal/..."}, {"bash", "make"}, {"bash", "ls -la /tmp"},
		{"edit_file", "/home/u/p/internal/tui/x.go"}, {"read_file", "/etc/hosts"},
		{"fetch_url", "https://pkg.go.dev/net/http"},
	} {
		for _, r := range SuggestLadder(c.tool, c.subject) {
			if c.tool == "bash" && (r.Pattern == "" || r.Pattern == "*") {
				t.Errorf("ladder(%s, %q) reaches %s: every bash call", c.tool, c.subject, r)
			}
			if !r.matches(c.tool, c.subject) {
				t.Errorf("rung %s does not match the call it came from (%s, %q)", r, c.tool, c.subject)
			}
		}
	}
	// The tree rung stops one level above the file's directory: it never
	// climbs to the root.
	for _, r := range SuggestLadder("edit_file", "/a/b.go") {
		if r.Pattern == "/*" {
			t.Fatal("ladder reached the root directory")
		}
	}
}
