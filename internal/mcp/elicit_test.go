package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const formSchema = `{"type":"object","required":["email","n"],"properties":{
 "email":{"type":"string","format":"email","title":"Email"},
 "n":{"type":"integer","minimum":1,"maximum":10},
 "ok":{"type":"boolean","default":true},
 "color":{"type":"string","enum":["r","g"],"enumNames":["Red","Green"]},
 "tags":{"type":"array","minItems":1,"items":{"enum":["a","b","c"]}},
 "when":{"type":"string","format":"date"},
 "name":{"type":"string","minLength":2,"maxLength":4}}}`

func TestParseFormKeepsOrderAndKinds(t *testing.T) {
	fs, err := ParseForm(json.RawMessage(formSchema))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range fs {
		names = append(names, f.Name+":"+string(f.Kind))
	}
	want := "email:string n:integer ok:boolean color:select tags:multiselect when:string name:string"
	if got := strings.Join(names, " "); got != want {
		t.Fatalf("got %s", got)
	}
	if !fs[0].Required || fs[2].Required || fs[3].Options[0].Label != "Red" {
		t.Fatalf("%+v", fs)
	}
}

func TestParseFormRefusals(t *testing.T) {
	big := `{"type":"object","properties":{` + strings.Repeat(`"a":{},`, 1) + `"z":{}}}`
	_ = big
	var many strings.Builder
	many.WriteString(`{"type":"object","properties":{`)
	for i := 0; i < 33; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"f` + strings.Repeat("x", i) + `":{"type":"string"}`)
	}
	many.WriteString("}}")
	for name, c := range map[string]string{
		"not object":   `[1]`,
		"wrong type":   `{"type":"array","properties":{}}`,
		"no props":     `{"type":"object"}`,
		"too many":     many.String(),
		"bad type":     `{"properties":{"a":{"type":"object"}}}`,
		"array noitem": `{"properties":{"a":{"type":"array"}}}`,
		"long name":    `{"properties":{"` + strings.Repeat("n", 65) + `":{}}}`,
	} {
		if _, err := ParseForm(json.RawMessage(c)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFieldValidate(t *testing.T) {
	fs, _ := ParseForm(json.RawMessage(formSchema))
	by := map[string]Field{}
	for _, f := range fs {
		by[f.Name] = f
	}
	for _, c := range []struct {
		field, in string
		want      any
		present   bool
		err       string
	}{
		{"email", "a@b.co", "a@b.co", true, ""},
		{"email", "nope", nil, false, "email"},
		{"email", "", nil, false, "required"},
		{"n", "5", int64(5), true, ""},
		{"n", "11", nil, false, "max"},
		{"n", "1.5", nil, false, "integer"},
		{"ok", "", true, true, ""},
		{"ok", "no", false, true, ""},
		{"ok", "maybe", nil, false, "yes or no"},
		{"color", "Green", "g", true, ""},
		{"color", "blue", nil, false, "choices"},
		{"color", "", nil, false, ""},
		{"tags", "a, c", []string{"a", "c"}, true, ""},
		{"tags", "", nil, false, "at least"},
		{"tags", "z", nil, false, "not one"},
		{"when", "2026-02-30", nil, false, "YYYY"},
		{"when", "2026-02-03", "2026-02-03", true, ""},
		{"name", "a", nil, false, "min length"},
		{"name", "abcde", nil, false, "max length"},
	} {
		v, present, err := by[c.field].Validate(c.in)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%s %q: err %v, want %q", c.field, c.in, err, c.err)
			}
			continue
		}
		if err != nil || present != c.present || !equalJSON(v, c.want) {
			t.Errorf("%s %q: %v %v %v", c.field, c.in, v, present, err)
		}
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func TestHandleElicit(t *testing.T) {
	good := json.RawMessage(`{"mode":"form","message":"hi","requestedSchema":{"properties":{"a":{"type":"string"}}}}`)
	for _, c := range []struct {
		name    string
		handler func(context.Context, ElicitRequest) ElicitResponse
		params  json.RawMessage
		want    string // substring of the result, or of the error
		isErr   bool
	}{
		{"no handler", nil, good, "not available", true},
		{"accept", func(context.Context, ElicitRequest) ElicitResponse {
			return ElicitResponse{Action: "accept", Content: map[string]any{"a": "x"}}
		}, good, `"action":"accept","content":{"a":"x"}`, false},
		{"decline drops content", func(context.Context, ElicitRequest) ElicitResponse {
			return ElicitResponse{Action: "decline", Content: map[string]any{"a": "x"}}
		}, good, `{"action":"decline"}`, false},
		{"junk action cancels", func(context.Context, ElicitRequest) ElicitResponse { return ElicitResponse{Action: "??"} }, good, `"cancel"`, false},
		{"url mode", func(context.Context, ElicitRequest) ElicitResponse { return ElicitResponse{} }, json.RawMessage(`{"mode":"url"}`), "form-mode", true},
		{"bad schema", func(context.Context, ElicitRequest) ElicitResponse { return ElicitResponse{} }, json.RawMessage(`{"requestedSchema":{"type":"array"}}`), "type", true},
	} {
		cl := &Client{onElicit: c.handler, done: make(chan struct{})}
		res, rerr := cl.handleElicit(c.params)
		got := string(res)
		if c.isErr {
			if rerr == nil {
				t.Errorf("%s: no error", c.name)
				continue
			}
			got = rerr.Message
		} else if rerr != nil {
			t.Errorf("%s: %v", c.name, rerr)
			continue
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: %q lacks %q", c.name, got, c.want)
		}
	}
}
