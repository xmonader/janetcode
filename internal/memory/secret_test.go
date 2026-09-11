package memory

import (
	"strings"
	"testing"
)

func TestScanSecretFindsCredentials(t *testing.T) {
	cases := []string{
		"the key is sk-ant-api03-" + strings.Repeat("A1b2", 8),
		"OPENAI_API_KEY=sk-proj-" + strings.Repeat("x7Yz", 10),
		"use AKIAIOSFODNN7EXAMPLE for the bucket",
		"token ghp_" + strings.Repeat("aB3d", 9),
		"-----BEGIN RSA PRIVATE KEY-----",
		"client_secret: aZ9" + strings.Repeat("Qw3e", 8),
	}
	for _, c := range cases {
		if got := ScanSecret(c); got == "" {
			t.Errorf("missed a credential in %.40q", c)
		}
	}
}

func TestScanSecretLeavesOrdinaryNotesAlone(t *testing.T) {
	cases := []string{
		"the build uses go test ./... and takes about four minutes",
		"API_KEY comes from the environment; see docs/setup.md",
		`api_key: "your-key-here"`,
		"api_key: ${OPENAI_API_KEY}",
		"password: changeme-in-production",
		"the migration file is db/migrations/0001_initial_schema.sql",
	}
	for _, c := range cases {
		if got := ScanSecret(c); got != "" {
			t.Errorf("%q was refused as %s", c, got)
		}
	}
}

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
