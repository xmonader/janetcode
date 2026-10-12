package tui

import "testing"

// A code block whose lines all fit is glamour's output as it is: trimming
// its padding and refitting it on every render cost about 8% of a full
// re-render and changed nothing that fits.
func TestCodeBlockThatFitsIsNotRefitted(t *testing.T) {
	tr := newTranscript()
	src := "```go\nx := 1\n```\n"
	out, err := tr.renderCodeBlock(src, 80)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tr.codeRenderer.Render(src)
	if err != nil {
		t.Fatal(err)
	}
	if out != raw {
		t.Fatalf("a block that fits was reworked:\n got %q\nwant %q", out, raw)
	}
}
