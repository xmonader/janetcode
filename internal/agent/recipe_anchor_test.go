package agent

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/recipe"
)

// replayEdits applies text-form edit_file steps in order the way replay
// does: each old_string must occur exactly once in the file as the
// earlier steps left it.
func replayEdits(t *testing.T, text string, steps []recipe.Step) (string, bool) {
	t.Helper()
	for i, st := range steps {
		var in struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal(st.Input, &in); err != nil || in.OldString == "" {
			t.Fatalf("step %d is not a text-form edit: %s", i+1, st.Input)
		}
		if strings.Count(text, in.OldString) != 1 {
			t.Logf("step %d: old_string %q occurs %d times in %q", i+1, in.OldString, strings.Count(text, in.OldString), text)
			return text, false
		}
		text = strings.Replace(text, in.OldString, in.NewString, 1)
	}
	return text, true
}

// Each step's anchor is looked for in the file as the steps before it
// left it. In a repetitive file the context one region needs can reach
// into a neighbouring region; anchored against the file as recorded,
// that step then carried the neighbour's old text, which the earlier step
// had already replaced, and the replay of a turn that worked stopped.
func TestRecipeAnchorsReplayInOrderOnARepetitiveFile(t *testing.T) {
	for _, c := range []struct{ before, after string }{
		{"x\nk\nx\nk\nx\nk\n", "y\nk\ny\nk\nx\nk\n"},
		{"x\nk\nx\nk\nx\nk\n", "x\nk\ny\nk\ny\nk\n"},
		{"x\nk\nx\nk\nx\nk\nx\n", "y\nk\nx\nk\ny\nk\nx\n"},
		{"a\nx\nb\nx\nc\n", "a\ny\nb\ny\nc\n"},
	} {
		a := &Agent{}
		st := recipe.Step{Tool: "write_file", Input: json.RawMessage(`{"path":"f.txt","content":""}`)}
		steps := a.hunksToEditSteps(st, Change{Path: "f.txt", Before: []byte(c.before), After: []byte(c.after)})
		if steps[0].Tool != "edit_file" {
			t.Fatalf("%q: not converted: %+v", c.after, steps)
		}
		got, ok := replayEdits(t, c.before, steps)
		if !ok || got != c.after {
			t.Errorf("%q -> %q: replay gave %q (ok=%v)", c.before, c.after, got, ok)
		}
	}
	// The same property over many small repetitive files: a converted
	// recipe either replays to exactly the recorded result or is kept as
	// the original step, never lands somewhere else.
	rng := rand.New(rand.NewSource(1))
	alphabet := []string{"x\n", "k\n", "y\n"}
	for n := 0; n < 3000; n++ {
		var b, a strings.Builder
		for i := 0; i < 3+rng.Intn(9); i++ {
			line := alphabet[rng.Intn(2)]
			b.WriteString(line)
			switch rng.Intn(5) {
			case 0:
				a.WriteString("y\n")
			case 1: // deleted
			case 2:
				a.WriteString(line + "k\n")
			default:
				a.WriteString(line)
			}
		}
		before, after := b.String(), a.String()
		if before == after {
			continue
		}
		st := recipe.Step{Tool: "write_file", Input: json.RawMessage(`{"path":"f.txt","content":""}`)}
		steps := (&Agent{}).hunksToEditSteps(st, Change{Path: "f.txt", Before: []byte(before), After: []byte(after)})
		if steps[0].Tool != "edit_file" {
			continue // kept as recorded: safe
		}
		if got, ok := replayEdits(t, before, steps); ok && got != after {
			t.Fatalf("%q -> %q: replay landed elsewhere: %q", before, after, got)
		}
	}
}
