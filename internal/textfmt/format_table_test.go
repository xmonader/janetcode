package textfmt

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The table the web page's format.js is checked against too: one set of
// numbers, whichever front-end prints them.
func TestFormatTable(t *testing.T) {
	data, err := os.ReadFile("testdata/format.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Duration [][2]any `json:"duration_ms"`
		Tokens   [][2]any `json:"tokens"`
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	for _, row := range table.Duration {
		ms := row[0].(float64)
		if got := Duration(time.Duration(ms * float64(time.Millisecond))); got != row[1] {
			t.Errorf("Duration(%vms) = %q, want %q", ms, got, row[1])
		}
	}
	for _, row := range table.Tokens {
		n := int(row[0].(float64))
		if got := Tokens(n); got != row[1] {
			t.Errorf("Tokens(%d) = %q, want %q", n, got, row[1])
		}
	}
}
