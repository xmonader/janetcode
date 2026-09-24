package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// A list or object sent as a string holding its JSON is taken as meant,
// for every tool, instead of costing the model a round trip.
func TestDecodeUnwrapsStringEncodedStructures(t *testing.T) {
	var in struct {
		Paths []string          `json:"paths"`
		Opts  struct{ N int }   `json:"opts"`
		Name  string            `json:"name"`
		Raw   json.RawMessage   `json:"raw"`
		Tags  map[string]string `json:"tags"`
	}
	err := decode(json.RawMessage(`{"paths":"[\"a.go\", \"b.go\"]","opts":" {\"N\":3} ","name":"[not a list]","raw":"[1]","tags":"{\"k\":\"v\"}"}`), &in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(in.Paths, ",") != "a.go,b.go" || in.Opts.N != 3 || in.Tags["k"] != "v" {
		t.Errorf("structures not unwrapped: %+v", in)
	}
	// A string field keeps its text, and a raw field keeps the string it was given.
	if in.Name != "[not a list]" || string(in.Raw) != `"[1]"` {
		t.Errorf("name %q raw %s", in.Name, in.Raw)
	}
}

// What cannot be unwrapped reports its own error; after an unwrap, the
// error is about what is still wrong.
func TestDecodeKeepsTheFirstError(t *testing.T) {
	var in struct {
		Paths []string `json:"paths"`
	}
	err := decode(json.RawMessage(`{"paths":"a.go"}`), &in)
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal string") {
		t.Fatalf("err = %v", err)
	}
	err = decode(json.RawMessage(`{"paths":"[\"a\"]","nope":1}`), &in)
	if err == nil || !strings.Contains(err.Error(), "this tool accepts: paths") {
		t.Fatalf("err = %v", err)
	}
}
