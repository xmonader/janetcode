package args

import (
	"errors"
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	spec := Spec{Bool: []string{"--dry"}, Value: []string{"--turn"}, Rest: []string{"--desc"}, KV: true}
	got, err := Tokenize("  deploy --turn 4 env=prod --dry extra --desc  the rest --turn 9 ", spec)
	if err != nil {
		t.Fatal(err)
	}
	want := Args{
		Positional: []string{"deploy", "extra"},
		Flags:      map[string]string{"--turn": "4", "--dry": "", "--desc": "the rest --turn 9"},
		KV:         map[string]string{"env": "prod"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	// A flag name inside a longer word is not the flag.
	got, err = Tokenize("fix--desc x--turn", spec)
	if err != nil || len(got.Flags) != 0 || !reflect.DeepEqual(got.Positional, []string{"fix--desc", "x--turn"}) {
		t.Fatalf("got %+v, %v", got, err)
	}
	var mv *MissingValueError
	if _, err := Tokenize("a --turn", spec); !errors.As(err, &mv) || mv.Flag != "--turn" {
		t.Fatalf("missing value: got %v", err)
	}
	if _, err := Tokenize("a --turn --desc x", spec); !errors.As(err, &mv) {
		t.Fatalf("value then rest flag: got %v", err)
	}
	var ek *EmptyKeyError
	if _, err := Tokenize("=v", spec); !errors.As(err, &ek) {
		t.Fatalf("empty key: got %v", err)
	}
	if got, _ := Tokenize("a=b", Spec{}); !reflect.DeepEqual(got.Positional, []string{"a=b"}) {
		t.Fatalf("KV off: got %+v", got)
	}
}
