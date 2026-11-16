package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestScriptCapturePreservesSmallOutputAcrossChunks(t *testing.T) {
	want := strings.Repeat("مرحبا\n", 12)
	b := scriptCapture{limit: 512}
	for i := range len(want) {
		if n, err := b.Write([]byte(want[i : i+1])); n != 1 || err != nil {
			t.Fatalf("write: %d, %v", n, err)
		}
	}
	if got := b.String(); got != want {
		t.Fatalf("small output changed: %q", got)
	}
}

func TestScriptCaptureBoundsMemoryAndPreservesEnds(t *testing.T) {
	b := scriptCapture{limit: 1000}
	b.Write([]byte("START\n"))
	chunk := []byte(strings.Repeat("س", 32000))
	for range 100 {
		b.Write(chunk)
	}
	b.Write([]byte("\nEND"))
	got := b.String()
	if len(b.head)+len(b.tail) > b.limit || cap(b.head)+cap(b.tail) > 3*b.limit {
		t.Fatal("capture grew with subprocess output")
	}
	if !strings.HasPrefix(got, "START\n") || !strings.HasSuffix(got, "\nEND") || !strings.Contains(got, "bytes of output dropped") || !utf8.ValidString(got) {
		t.Fatalf("lost output evidence: %q", got)
	}
}

func TestScriptNoisyFailureKeepsBoundedDiagnostics(t *testing.T) {
	pythonOrSkip(t)
	_, s := scriptAgent(t, t.TempDir())
	out := runScript(t, s, `
import sys
print("START")
print("x" * 5_000_000)
print("END")
sys.stderr.write("e" * 5_000_000 + "FINAL FAILURE\n")
sys.exit(1)
`)
	if len(out) > scriptMaxOutput+5000 || !strings.Contains(out, "START") || !strings.Contains(out, "END") || !strings.Contains(out, "FINAL FAILURE") || !strings.Contains(out, "exit status 1") {
		t.Fatalf("noisy failure lost diagnostics or exceeded budget: %d bytes, suffix %q", len(out), out[max(0, len(out)-300):])
	}
}
