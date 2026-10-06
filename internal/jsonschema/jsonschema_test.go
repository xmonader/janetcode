package jsonschema

import (
	"strings"
	"testing"
)

const person = `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "age": {"type": "integer", "minimum": 0},
    "role": {"enum": ["admin", "user"]},
    "tags": {"type": "array", "items": {"type": "string"}, "maxItems": 2},
    "address": {"$ref": "#/$defs/Address"}
  },
  "required": ["name", "age"],
  "additionalProperties": false,
  "$defs": {
    "Address": {
      "type": "object",
      "properties": {"zip": {"type": "string", "pattern": "^[0-9]{5}$"}},
      "required": ["zip"]
    }
  }
}`

func TestValidate(t *testing.T) {
	s, err := Compile([]byte(person))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		doc  string
		want []string // substrings of the error; empty means valid
	}{
		{`{"name":"a","age":3}`, nil},
		{`{"name":"a","age":3.0,"role":"user","tags":["x"],"address":{"zip":"12345"}}`, nil},
		{`{"name":"a"}`, []string{`$: missing required property "age"`}},
		{`{"name":"","age":-1}`, []string{"$.name: shorter than 1", "$.age: less than minimum 0"}},
		{`{"name":"a","age":1.5}`, []string{"$.age: expected integer, got number"}},
		{`{"name":"a","age":1,"role":"root"}`, []string{`$.role: must be one of ["admin","user"]`}},
		{`{"name":"a","age":1,"x":1}`, []string{`unexpected property "x"`}},
		{`{"name":"a","age":1,"tags":["a",2,"c"]}`, []string{"$.tags: more than 2 items", "$.tags[1]: expected string"}},
		{`{"name":"a","age":1,"address":{"zip":"abc"}}`, []string{`$.address.zip: does not match pattern`}},
		{`{"name":"a","age":1,"address":{}}`, []string{`$.address: missing required property "zip"`}},
		{`[1]`, []string{"$: expected object, got array"}},
		{`{"name":"a","age":1} {}`, []string{"trailing data"}},
	} {
		err := s.Validate([]byte(tc.doc))
		if len(tc.want) == 0 {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.doc, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: valid, want errors %q", tc.doc, tc.want)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error %q lacks %q", tc.doc, err, w)
			}
		}
	}
}

func TestCombinators(t *testing.T) {
	s, err := Compile([]byte(`{"oneOf":[{"type":"string"},{"type":"integer"},{"type":"number","minimum":10}],"not":{"const":"no"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate([]byte(`"yes"`)); err != nil {
		t.Errorf("string: %v", err)
	}
	if err := s.Validate([]byte(`"no"`)); err == nil {
		t.Error(`"no" passed the not schema`)
	}
	// 12 is an integer and a number >= 10: two alternatives match.
	if err := s.Validate([]byte(`12`)); err == nil || !strings.Contains(err.Error(), "matches 2") {
		t.Errorf("12: %v", err)
	}
	if err := s.Validate([]byte(`true`)); err == nil {
		t.Error("true matched a oneOf with no boolean branch")
	}
	any, _ := Compile([]byte(`{"anyOf":[{"type":"null"},{"type":"string"}]}`))
	if err := any.Validate([]byte(`null`)); err != nil {
		t.Errorf("null: %v", err)
	}
	if err := any.Validate([]byte(`1`)); err == nil {
		t.Error("1 matched anyOf null|string")
	}
}

func TestCompileErrors(t *testing.T) {
	for doc, want := range map[string]string{
		`{`:                                     "not valid JSON",
		`[]`:                                    "must be a JSON object",
		`{"$ref":"#/$defs/Missing","$defs":{}}`: `"Missing" not found`,
		`{"$ref":"other.json#/x"}`:              "only local references",
		`{"properties":{"a":{"pattern":"("}}}`:  "pattern",
	} {
		_, err := Compile([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", doc, err, want)
		}
	}
}

func TestRecursiveRefTerminates(t *testing.T) {
	s, err := Compile([]byte(`{"type":"object","properties":{"child":{"$ref":"#"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate([]byte(`{"child":{"child":{"child":1}}}`)); err == nil || !strings.Contains(err.Error(), "$.child.child.child: expected object") {
		t.Errorf("got %v", err)
	}
}

func TestBooleanSchema(t *testing.T) {
	s, err := Compile([]byte(`{"type":"object","additionalProperties":{"type":"number"},"properties":{"x":false}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate([]byte(`{"y":1}`)); err != nil {
		t.Error(err)
	}
	if err := s.Validate([]byte(`{"y":"s"}`)); err == nil {
		t.Error("additionalProperties schema not applied")
	}
	if err := s.Validate([]byte(`{"x":1}`)); err == nil {
		t.Error("false schema allowed a value")
	}
}
