package statusline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTheCommandReadsTheSessionAndPrintsTheLine(t *testing.T) {
	in := Input{Cwd: "/w", Model: Model{ID: "m1", DisplayName: "m1"}, Git: &Git{Branch: "main"}}
	in.ContextWindow.SetUsage(25_000, 100_000)
	c := Config{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print(d["hook_event_name"], d["model"]["display_name"], d["git"]["branch"], d["context_window"]["used_percentage"])'`}
	lines, err := Run(context.Background(), c, t.TempDir(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0] != "Status m1 main 25" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestAFieldJanetcodeCannotFillIsLeftOut(t *testing.T) {
	var in Input
	in.ContextWindow.SetUsage(100, 0) // no window known: no percentages
	data, _ := json.Marshal(in)
	for _, absent := range []string{"used_percentage", `"git"`, `"cost"`} {
		if strings.Contains(string(data), absent) {
			t.Fatalf("%s present in %s", absent, data)
		}
	}
}

func TestOutputIsTwoLinesOfTextAndColour(t *testing.T) {
	out := "\n\x1b[32mgreen\x1b[0m\ttab\x1b]0;evil title\x07\x1b[2J\x1b[H\r\n\n  \nsecond\nthird\n"
	lines := Lines(out)
	if len(lines) != 2 || lines[0] != "\x1b[32mgreen\x1b[0m tab" || lines[1] != "second" {
		t.Fatalf("lines = %q", lines)
	}
}

// TestTakingASequenceOutCannotMakeAnother: the escapes were removed and
// whatever escape bytes were left were kept, so dropping the sequence in
// the middle of "\x1b[2" "\x1bX" "J" joined its neighbours into
// "\x1b[2J", which clears the screen.
func TestTakingASequenceOutCannotMakeAnother(t *testing.T) {
	for _, in := range []string{
		"a\x1b[2\x1bXJb",
		"a\x1b[2\x1b]0;t\x07Jb",
		"a\x1b\x1b[Hb",
		"a\x1b[\x1b[31mb",
		"a\x1b",
	} {
		got := clean(in)
		if strings.Contains(stripSGR.ReplaceAllString(got, ""), "\x1b") {
			t.Errorf("clean(%q) = %q: an escape other than a colour survived", in, got)
		}
	}
	if got := clean("\x1b[31mred\x1b[0m"); got != "\x1b[31mred\x1b[0m" {
		t.Fatalf("colour lost: %q", got)
	}
}

func TestASlowOrFailingCommandIsAnError(t *testing.T) {
	if _, err := Run(context.Background(), Config{Command: "echo nope >&2; exit 1"}, t.TempDir(), Input{}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v", err)
	}
	if testing.Short() {
		return
	}
	start := time.Now()
	_, err := Run(context.Background(), Config{Command: "sleep 30 & wait"}, t.TempDir(), Input{})
	if err == nil || !strings.Contains(err.Error(), "timed out") || time.Since(start) > Timeout+3*time.Second {
		t.Fatalf("err = %v after %s", err, time.Since(start))
	}
}

func TestTheirConfigObjectsAreRead(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"type":"command","command":"date","refreshInterval":2,"padding":0}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Command != "date" || c.Refresh() != 2*time.Second || c.Validate() != nil {
		t.Fatalf("config = %+v", c)
	}
	if err := (Config{Type: "preset", Command: "x"}).Validate(); err == nil {
		t.Fatal("preset accepted")
	}
	if err := (Config{}).Validate(); err == nil {
		t.Fatal("empty command accepted")
	}
}
