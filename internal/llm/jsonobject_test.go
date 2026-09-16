package llm

import "testing"

func TestExtractJSONObject(t *testing.T) {
	type answer struct {
		Verdict string `json:"verdict"`
		Score   int    `json:"score"`
	}
	for _, tc := range []struct {
		name, text string
		want       answer
		wantErr    bool
	}{
		{name: "bare object", text: `{"verdict":"ok","score":3}`, want: answer{"ok", 3}},
		{name: "code fence and prose", text: "Sure!\n```json\n{\"verdict\":\"ok\",\"score\":3}\n```\nHope that helps.", want: answer{"ok", 3}},
		{name: "nested object", text: `{"verdict":"ok","score":3,"extra":{"a":{"b":1}}}`, want: answer{"ok", 3}},
		// The span from the first brace to the last one is not JSON here;
		// the object the model was asked for still is.
		{name: "braces in the prose before it", text: `I would say {maybe}, but here it is: {"verdict":"ok","score":3}`, want: answer{"ok", 3}},
		{name: "a second object after it", text: `{"verdict":"ok","score":3} and for comparison {"verdict":"no"}`, want: answer{"ok", 3}},
		{name: "a brace inside a string is not a brace", text: `Note: {"verdict":"ok {not an object","score":3}`, want: answer{"ok {not an object", 3}},
		{name: "an escaped quote does not end the string", text: `x {"verdict":"say \"hi\" }","score":3}`, want: answer{`say "hi" }`, 3}},
		{name: "no object at all", text: "there is no JSON here", wantErr: true},
		{name: "only an opening brace", text: "{ nope", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got answer
			err := ExtractJSONObject(tc.text, &got)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A reply that is nothing but braces must not be walked forever.
func TestExtractJSONObjectBoundsTheScan(t *testing.T) {
	var got map[string]any
	text := ""
	for range 5000 {
		text += "{"
	}
	if err := ExtractJSONObject(text+"}", &got); err == nil {
		t.Fatal("want an error")
	}
}
