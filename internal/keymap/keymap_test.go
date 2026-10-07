package keymap

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultsResolve(t *testing.T) {
	m := Default()
	if a, _ := m.Lookup("ctrl+r"); a != HistorySearch {
		t.Fatalf("ctrl+r = %q, want history_search", a)
	}
	if a, _ := m.Lookup("alt+e"); a != Effort {
		t.Fatalf("alt+e = %q, want effort", a)
	}
	if a, _ := m.Lookup("ctrl+x ctrl+e"); a != ExternalEditor || !m.IsPrefix("ctrl+x") {
		t.Fatalf("the external editor chord is not bound")
	}
	if len(m.Changed()) != 0 {
		t.Fatalf("defaults report changes: %v", m.Changed())
	}
}

func parse(t *testing.T, s string) Overrides {
	t.Helper()
	var ov Overrides
	if err := json.Unmarshal([]byte(s), &ov); err != nil {
		t.Fatal(err)
	}
	return ov
}

func TestOverrideReplacesDefaults(t *testing.T) {
	m, err := Resolve(parse(t, `{"effort": ["alt+r", "f2"], "history_search": "ctrl+r", "agents": ""}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Lookup("alt+e"); ok {
		t.Fatal("the default key of an overridden action is still bound")
	}
	if a, _ := m.Lookup("f2"); a != Effort {
		t.Fatalf("f2 = %q", a)
	}
	if _, ok := m.Lookup("ctrl+t"); ok || m.Label(Agents) != "unbound" {
		t.Fatal("\"\" did not unbind agents")
	}
	if got := m.Changed(); len(got) != 2 {
		t.Fatalf("changed = %v, want effort and agents", got)
	}
}

func TestResolveRefuses(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`{"efort": "alt+r"}`, `unknown action "efort"`},
		{`{"effort": "ctrl+t"}`, "bound to both"},
		{`{"effort": "x"}`, "plain character"},
		{`{"effort": "ctrl+nope"}`, "not a key name"},
		{`{"effort": "ctrl+x ctrl+y ctrl+z"}`, "at most two keys"},
		{`{"effort": "ctrl+x"}`, "starts a chord"},
		{`{"effort": 3}`, "want a key"},
	} {
		var ov Overrides
		err := json.Unmarshal([]byte(tc.json), &ov)
		if err == nil {
			_, err = Resolve(ov)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.json, err, tc.want)
		}
	}
	// Moving a default key to another action is allowed when its old
	// action moves too.
	if _, err := Resolve(parse(t, `{"effort": "ctrl+t", "agents": "alt+a"}`)); err != nil {
		t.Fatalf("a swap was refused: %v", err)
	}
}
