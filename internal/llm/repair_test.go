package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRepairJSON(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"rows":[[1],[2]}`, `{"rows":[[1],[2]]}`},
		{`{"a":{"b":1}`, `{"a":{"b":1}}`},
		{`{"a":[1,2,]}`, `{"a":[1,2]}`},
		{`{"a":[1,2,`, `{"a":[1,2]}`},
		{`{"s":"}]{[","n":[1}`, `{"s":"}]{[","n":[1]}`},
		{`{"s":"a\"}"`, `{"s":"a\"}"}`},
	}
	for _, c := range cases {
		got, ok := RepairJSON(c.in)
		if !ok || got != c.want {
			t.Errorf("RepairJSON(%s) = %q, %v; want %q", c.in, got, ok, c.want)
		}
	}
}

// A string still open at the end is what a reply cut off mid-value looks
// like; closing it would make a half-written file look like a whole one.
func TestRepairJSONRefusesWhatItCannotMend(t *testing.T) {
	for _, in := range []string{
		`{"path":"a.go","content":"package ma`,
		`{"a":1}}`,
		`{"a":1]`,
		`not json`,
		`{"a" 1}`,
		`{"a":` + strings.Repeat("[", 10) + `}`,
	} {
		if got, ok := RepairJSON(in); ok {
			t.Errorf("RepairJSON(%s) = %q, want refusal", in, got)
		}
	}
}

func TestInvalidArgumentsRoundTrip(t *testing.T) {
	raw := `{"path": "a.go", oops`
	in := InvalidArguments(raw)
	if !json.Valid(in) {
		t.Fatalf("wrapper is not JSON: %s", in)
	}
	got, ok := UnparsedArguments(in)
	if !ok || got != raw {
		t.Fatalf("UnparsedArguments = %q, %v", got, ok)
	}
	if _, ok := UnparsedArguments(json.RawMessage(`{"path":"a.go"}`)); ok {
		t.Fatal("ordinary arguments reported as unparsed")
	}
}
