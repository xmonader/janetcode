package tui

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
)

// /permissions allow accepts a parameter rule, spaces and all: the
// command joins its arguments back together, so the comma-separated form
// survives being split into words.
func TestPermissionsCommandAcceptsParamRules(t *testing.T) {
	h := newHarness(t)
	ag := h.m.opts.Agent
	h.m.permissionsCommand([]string{"allow", "task(agent:explore,", "model:haiku*)"})
	h.m.permissionsCommand([]string{"deny", "fetch_url(domain:*.internal.example)"})
	rs := ag.RulesSnapshot()
	if len(rs.Allow) != 1 || rs.Allow[0].String() != "task(agent:explore, model:haiku*)" {
		t.Fatalf("allow rules = %+v", rs.Allow)
	}
	if len(rs.Deny) != 1 || rs.Deny[0].String() != "fetch_url(domain:*.internal.example)" {
		t.Fatalf("deny rules = %+v", rs.Deny)
	}
	// And the list shows them back.
	h.m.permissionsCommand(nil)
	var body strings.Builder
	for _, e := range h.m.transcript.entries {
		body.WriteString(e.text + "\n")
	}
	for _, want := range []string{"allow  task(agent:explore, model:haiku*)", "deny   fetch_url(domain:*.internal.example)"} {
		if !strings.Contains(body.String(), want) {
			t.Errorf("/permissions did not list %q", want)
		}
	}
	// A malformed one is refused with the parser's message, not saved.
	h.m.permissionsCommand([]string{"allow", "task(agent:explore,", "model)"})
	if len(ag.RulesSnapshot().Allow) != 1 {
		t.Fatal("a malformed parameter rule was saved")
	}
	if _, err := perm.Parse("task(agent:explore, model)"); err == nil {
		t.Fatal("the parser accepts the malformed rule after all")
	}
}
