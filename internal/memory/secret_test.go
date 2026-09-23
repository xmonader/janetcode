package memory

import (
	"strings"
	"testing"
)

func TestPutRefusesAnEntryHoldingASecret(t *testing.T) {
	s := Open(t.TempDir(), t.TempDir())
	_, _, err := s.Put(Entry{Description: "the staging key", Body: "ANTHROPIC_API_KEY=sk-ant-api03-" + strings.Repeat("A1b2", 8)})
	if err == nil {
		t.Fatal("a credential was written to memory")
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Fatalf("err = %v", err)
	}
	if len(s.Entries()) != 0 {
		t.Fatalf("entries = %+v", s.Entries())
	}
	// Saying where the value lives is exactly what should be remembered.
	if _, _, err := s.Put(Entry{Description: "staging key lives in 1Password, exported as ANTHROPIC_API_KEY"}); err != nil {
		t.Fatalf("a note about where the key lives was refused: %v", err)
	}
}
