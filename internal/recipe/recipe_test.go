package recipe

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func msg(text string, synthetic bool) *llm.Message {
	return &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: text, Synthetic: synthetic}}}
}

func TestFromEventsFiltersAndOrders(t *testing.T) {
	events := []session.Event{
		{Type: session.TypeTurnStart, Turn: 1},
		{Type: session.TypeUserMessage, Turn: 1, Message: msg("do the thing", false)},
		{Type: session.TypeToolCall, Turn: 1, CallID: "a", Name: "read_file", Arguments: `{"path":"x.go"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "a", Output: "package x\n"},
		{Type: session.TypeToolCall, Turn: 1, CallID: "b", Name: "bash", Arguments: `{"command":"go build ./..."}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "b", Output: "boom", IsError: true},
		{Type: session.TypeToolCall, Turn: 1, CallID: "c", Name: "task", Arguments: `{"description":"x","prompt":"y"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "c", Output: "ok"},
		{Type: session.TypeToolCall, Turn: 1, CallID: "d", Name: "write_file", Arguments: `{"path":"y.go","content":"package y\n"}`},
		{Type: session.TypeToolResult, Turn: 1, CallID: "d", Output: "wrote y.go"},
	}
	steps, err := FromEvents(events, 1)
	if err != nil {
		t.Fatal(err)
	}
	// bash's call failed (dropped), task is not replayable (dropped): only
	// read_file and write_file survive, in the order they ran.
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2: %+v", len(steps), steps)
	}
	if steps[0].Tool != "read_file" || steps[1].Tool != "write_file" {
		t.Fatalf("wrong order/tools: %+v", steps)
	}

	t.Run("turn zero picks the latest turn with tool calls", func(t *testing.T) {
		ev2 := append(append([]session.Event(nil), events...),
			session.Event{Type: session.TypeTurnStart, Turn: 2},
			session.Event{Type: session.TypeToolCall, Turn: 2, CallID: "e", Name: "grep", Arguments: `{"pattern":"foo"}`},
			session.Event{Type: session.TypeToolResult, Turn: 2, CallID: "e", Output: "found"},
		)
		steps, err := FromEvents(ev2, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(steps) != 1 || steps[0].Tool != "grep" {
			t.Fatalf("got %+v, want the turn 2 grep call", steps)
		}
	})

	t.Run("no tool calls at all is an error", func(t *testing.T) {
		if _, err := FromEvents(nil, 0); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("a turn with only unreplayable or failed calls is an error", func(t *testing.T) {
		only := []session.Event{
			{Type: session.TypeToolCall, Turn: 5, CallID: "z", Name: "task", Arguments: `{}`},
			{Type: session.TypeToolResult, Turn: 5, CallID: "z", Output: "ok"},
		}
		if _, err := FromEvents(only, 5); err == nil {
			t.Fatal("want an error")
		}
	})
}

func TestTurnSummarySkipsSynthetic(t *testing.T) {
	events := []session.Event{
		{Type: session.TypeUserMessage, Turn: 1, Message: msg("[a harness note]", true)},
		{Type: session.TypeUserMessage, Turn: 1, Message: msg("fix the flaky test\nand nothing else", false)},
	}
	got := TurnSummary(events, 1)
	if got != "fix the flaky test" {
		t.Fatalf("got %q", got)
	}
}

func TestParameteriseAndExpandRoundTrip(t *testing.T) {
	// The content carries a quote, a backslash-escaped quote and a real
	// newline, so a broken re-escape would show up as mangled JSON or a
	// wrong string once decoded.
	originalContent := "env: staging\nquote: \"say \\\"hi\\\"\"\nnext line"
	inputJSON, err := json.Marshal(map[string]string{"path": "config/staging.yaml", "content": originalContent})
	if err != nil {
		t.Fatal(err)
	}
	steps := []Step{
		{Tool: "write_file", Input: inputJSON},
		{Tool: "bash", Input: json.RawMessage(`{"command":"deploy.sh staging"}`)},
	}
	params := map[string]string{"env": "staging"}
	ps2, ps := Parameterise(steps, params)
	if len(ps) != 1 || ps[0].Name != "env" || ps[0].Default != "staging" {
		t.Fatalf("got params %+v", ps)
	}
	for _, st := range ps2 {
		if !json.Valid(st.Input) {
			t.Fatalf("parameterised step is not valid JSON: %s", st.Input)
		}
	}
	if !strings.Contains(string(ps2[0].Input), "{{env}}") {
		t.Fatalf("expected {{env}} in %s", ps2[0].Input)
	}

	r := Recipe{Name: "deploy", Params: ps, Steps: ps2}
	expanded, err := Expand(r, map[string]string{"env": "prod"})
	if err != nil {
		t.Fatal(err)
	}
	var content struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(expanded[0].Input, &content); err != nil {
		t.Fatal(err)
	}
	if content.Path != "config/prod.yaml" {
		t.Fatalf("path not substituted: %+v", content)
	}
	wantContent := strings.ReplaceAll(originalContent, "staging", "prod")
	if content.Content != wantContent {
		t.Fatalf("quotes/newlines mangled:\ngot  %q\nwant %q", content.Content, wantContent)
	}

	// Using the default (no arg given) round-trips back to the original.
	def, err := Expand(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mustUnmarshal(t, def[0].Input), mustUnmarshal(t, steps[0].Input)) {
		t.Fatalf("default expansion did not round-trip: %s vs %s", def[0].Input, steps[0].Input)
	}
}

func TestExpandErrorsOnUnknownAndMissing(t *testing.T) {
	r := Recipe{
		Name:   "r",
		Params: []Param{{Name: "env", Default: ""}},
		Steps:  []Step{{Tool: "bash", Input: json.RawMessage(`{"command":"deploy {{env}}"}`)}},
	}
	if _, err := Expand(r, map[string]string{"nope": "x"}); err == nil {
		t.Fatal("want an error for an unknown arg")
	}
	if _, err := Expand(r, nil); err == nil {
		t.Fatal("want an error for a required parameter with no default and no arg")
	}
	if _, err := Expand(r, map[string]string{"env": "prod"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	orphan := Recipe{Name: "r2", Steps: []Step{{Tool: "bash", Input: json.RawMessage(`{"command":"{{ghost}}"}`)}}}
	if _, err := Expand(orphan, nil); err == nil {
		t.Fatal("want an error for a template placeholder with no declared parameter")
	}
}

// Braces the recorded turn itself wrote (a Vue or Jinja template, a
// mustache file) are text, not placeholders: a saved recipe replays them
// as they were, with or without parameters, and a parameter that shares
// a template variable's name does not rewrite it.
func TestRecordedBracesAreNotPlaceholders(t *testing.T) {
	content := "<p>{{ message }}</p>\n{{env}} {{{{raw}}}} {{\"{{\"}}\nstaging"
	input, _ := json.Marshal(map[string]string{"path": "staging/view.vue", "content": content})
	steps := []Step{{Tool: "write_file", Input: input}}
	for _, params := range []map[string]string{nil, {"env": "staging"}} {
		saved, ps := Parameterise(steps, params)
		r := Recipe{Name: "tpl", Params: ps, Steps: saved}
		got, err := Expand(r, nil)
		if err != nil {
			t.Fatalf("params %v: %v", params, err)
		}
		if !reflect.DeepEqual(mustUnmarshal(t, got[0].Input), mustUnmarshal(t, input)) {
			t.Fatalf("params %v: replayed %s, recorded %s", params, got[0].Input, input)
		}
		if len(ps) == 0 {
			continue
		}
		got, err = Expand(r, map[string]string{"env": "prod"})
		if err != nil {
			t.Fatal(err)
		}
		var in struct{ Path, Content string }
		json.Unmarshal(got[0].Input, &in)
		if in.Path != "prod/view.vue" || in.Content != strings.ReplaceAll(content, "staging", "prod") {
			t.Fatalf("substituted %+v", in)
		}
	}
}

func mustUnmarshal(t *testing.T, raw json.RawMessage) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestParseSaveArgs(t *testing.T) {
	cases := []struct {
		arg      string
		wantName string
		wantTurn int
		wantErr  bool
	}{
		{"deploy", "deploy", 0, false},
		{"deploy --turn 4", "deploy", 4, false},
		{"deploy --turn 4 env=prod region=us", "deploy", 4, false},
		{"deploy --turn abc", "", 0, true},
		{"", "", 0, true},
		{"deploy notkv", "", 0, true},
	}
	for _, c := range cases {
		name, turn, params, err := ParseSaveArgs(c.arg)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got none", c.arg)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", c.arg, err)
			continue
		}
		if name != c.wantName || turn != c.wantTurn {
			t.Errorf("%q: got name=%q turn=%d, want name=%q turn=%d", c.arg, name, turn, c.wantName, c.wantTurn)
		}
		_ = params
	}
	_, _, params, err := ParseSaveArgs("deploy env=prod region=us")
	if err != nil {
		t.Fatal(err)
	}
	if params["env"] != "prod" || params["region"] != "us" {
		t.Fatalf("got params %+v", params)
	}
}

func TestParseRunArgs(t *testing.T) {
	name, args, dry, err := ParseRunArgs("deploy --dry-run env=prod")
	if err != nil {
		t.Fatal(err)
	}
	if name != "deploy" || !dry || args["env"] != "prod" {
		t.Fatalf("got name=%q dry=%v args=%+v", name, dry, args)
	}
	if _, _, _, err := ParseRunArgs(""); err == nil {
		t.Fatal("want error on empty arg")
	}
	if _, _, _, err := ParseRunArgs("deploy notkv"); err == nil {
		t.Fatal("want error on a bad token")
	}
}

func TestCutDesc(t *testing.T) {
	rest, desc := CutDesc("deploy --turn 4 --desc a real description here")
	if rest != "deploy --turn 4" || desc != "a real description here" {
		t.Fatalf("got rest=%q desc=%q", rest, desc)
	}
	rest, desc = CutDesc("deploy --turn 4")
	if rest != "deploy --turn 4" || desc != "" {
		t.Fatalf("got rest=%q desc=%q", rest, desc)
	}
}

func TestSaveLoadList(t *testing.T) {
	root := t.TempDir()
	r := Recipe{Name: "deploy", Description: "deploys staging", Steps: []Step{
		{Tool: "bash", Input: json.RawMessage(`{"command":"echo hi"}`)},
	}}
	path, err := Save(root, r)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "deploy.json" {
		t.Fatalf("unexpected path %s", path)
	}
	if _, err := Save(root, r); err == nil {
		t.Fatal("want an error overwriting an existing recipe")
	}
	got, err := Load(root, "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Description != "deploys staging" || len(got.Steps) != 1 {
		t.Fatalf("got %+v", got)
	}
	if _, err := Load(root, "nope"); err == nil {
		t.Fatal("want an error for a missing recipe")
	}

	r2 := Recipe{Name: "other", Steps: r.Steps}
	if _, err := Save(root, r2); err != nil {
		t.Fatal(err)
	}
	list, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "deploy" || list[1].Name != "other" {
		t.Fatalf("got %+v", list)
	}
}

// Parameterise orders its substitutions longest value first, and the
// slice it orders comes out of a map. Two parameters with equally long
// values therefore substituted in whichever order Go's map randomisation
// gave that run, so saving the same recipe twice could produce different
// steps and a different params list.
func TestParameteriseIsDeterministic(t *testing.T) {
	steps := []Step{{Tool: "bash", Input: json.RawMessage(`{"command":"deploy alpha beta alphabeta"}`)}}
	// Equally long values, and one contained in the text beside the other.
	params := map[string]string{"a": "alpha", "b": "gamma", "c": "delta", "d": "kappa", "e": "sigma"}
	wantSteps, wantParams := Parameterise(steps, params)
	for i := 0; i < 20; i++ {
		gotSteps, gotParams := Parameterise(steps, params)
		if string(gotSteps[0].Input) != string(wantSteps[0].Input) {
			t.Fatalf("substitution order moved: %s then %s", wantSteps[0].Input, gotSteps[0].Input)
		}
		if len(gotParams) != len(wantParams) {
			t.Fatalf("params list moved: %+v then %+v", wantParams, gotParams)
		}
		for j := range wantParams {
			if gotParams[j] != wantParams[j] {
				t.Fatalf("params order moved: %+v then %+v", wantParams, gotParams)
			}
		}
	}
}

func TestListOnMissingDirIsEmptyNotError(t *testing.T) {
	list, err := List(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("got %+v", list)
	}
}

func TestLoadSaysWhatIsSaved(t *testing.T) {
	root := t.TempDir()
	if _, err := Load(root, "x"); !errors.Is(err, ErrNoRecipe) || !strings.Contains(err.Error(), "none are saved") {
		t.Fatalf("empty project: %v", err)
	}
	if _, err := Save(root, Recipe{Name: "fmt", Steps: []Step{{Tool: "bash", Input: []byte(`{"command":"gofmt -l ."}`)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, "x"); !errors.Is(err, ErrNoRecipe) || err.Error() != `no recipe named "x"; saved: fmt` {
		t.Fatalf("one saved: %v", err)
	}
}
