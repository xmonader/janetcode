package present

import (
	"strings"
	"testing"
)

// Each mode says what runs and what asks in the same words, and the
// facts that change it (rules, session allowances, protected paths,
// sandbox) each show up where they apply.
func TestPermissionSummary(t *testing.T) {
	for _, c := range []struct {
		name    string
		p       Permissions
		want    []string
		notWant []string
	}{
		{"ask", Permissions{Protected: 20},
			[]string{"ask: ", "runs         reading and searching", "asks first   file edits", "shell commands", "protected paths", "never runs   commands that would wreck", "on this machine, as you"},
			[]string{"allow rule", "deny rule"}},
		{"auto-edits", Permissions{AutoEdits: true},
			[]string{"auto-edits: ", "file edits\n", "asks first   shell commands"},
			[]string{"asks first   file edits"}},
		{"auto", Permissions{AutoMode: true},
			[]string{"auto: ", "read-only commands", "other commands"},
			nil},
		{"yolo", Permissions{AutoApprove: true, Protected: 20, Allow: []string{"bash(go test *)"}},
			[]string{"yolo: ", "everything else", "asks first   protected paths"},
			[]string{"shell commands", "allow rule below"}},
		{"read-only", Permissions{ReadOnly: true},
			[]string{"read-only: "},
			[]string{"asks first"}},
		{"rules, session, sandbox", Permissions{Allow: []string{"bash(go test *)"}, Deny: []string{"bash(rm *)"}, SessionAllowed: []string{"bash"}, Sandbox: "workspace"},
			[]string{"what the 1 allow rule below match", "bash, allowed for this session", "what the 1 deny rule below match", "deny   bash(rm *)", "allow  bash(go test *)", "in the workspace sandbox"},
			[]string{"no sandbox"}},
	} {
		got := PermissionSummary(c.p, "change it here")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: lacks %q:\n%s", c.name, w, got)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: has %q:\n%s", c.name, w, got)
			}
		}
		if !strings.HasSuffix(got, "change it here") {
			t.Errorf("%s: no change line", c.name)
		}
		for _, l := range strings.Split(got, "\n") {
			if len([]rune(l)) > 78 {
				t.Errorf("%s: %d columns: %q", c.name, len([]rune(l)), l)
			}
		}
	}
}
