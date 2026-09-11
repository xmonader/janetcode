package perm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
		{"bash", "make", "bash(make)"},
		{"bash", "ls -la /tmp", "bash(ls *)"},
		{"bash", "make build", "bash(make *)"},
		{"bash", "python3 -m pytest", "bash(python3 *)"},
		{"bash", "git commit -m x", "bash(git commit *)"},
		{"bash", "./scripts/run.sh --all", "bash(./scripts/run.sh *)"},
		{"fetch_url", "https://pkg.go.dev/net/http?tab=doc", "fetch_url(https://pkg.go.dev/*)"},
		{"edit_file", "internal/agent/x.go", "edit_file(internal/agent/*)"},
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
