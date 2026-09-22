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

// A recorded eval run carried a shell's whole environment out of the
// sandbox: the vendor patterns named every key shape but the two that
// were actually there — a bare sk- key and a variable whose name says
// "key" without ending in _API_KEY. These are the shapes, with fake
// values of the right form.
func TestRedactSecretsCatchesGenericKeyShapes(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"DEEPSEEK_API_KEY_PERSONAL=sk-dcaf42a37674b1e9c0d5a8f3b27e6104",
			"DEEPSEEK_API_KEY_PERSONAL=[redacted]", 1},
		{"export DASHSCOPE_KEY=sk-sp-6133bcaa91de40f7b8c2", "export DASHSCOPE_KEY=[redacted]", 1},
		{"the key is sk-6133bcaa91de40f7b8c2e5", "the key is [redacted]", 1},
		{"MY_SERVICE_TOKEN='tok_9f3a2b7c1d4e'", "MY_SERVICE_TOKEN=[redacted]", 1},
		{"curl -H 'x-api-key: 9f3a2b7c1d4e5f60' https://x", "curl -H 'x-api-key: [redacted]' https://x", 1},
		{"Authorization: Bearer 9f3a2b7c1d4e5f6071", "Authorization: Bearer [redacted]", 1},
	}
	for _, c := range cases {
		got, n := RedactSecrets(c.in)
		if got != c.want || n != c.n {
			t.Errorf("RedactSecrets(%q) = %q, %d; want %q, %d", c.in, got, n, c.want, c.n)
		}
		if ScanSecret(c.in) == "" {
			t.Errorf("ScanSecret missed %q", c.in)
		}
	}
}

// The cost of a wider net is what it catches by mistake, so each of
// these is a shape that reads like a secret and is not one.
func TestRedactSecretsLeavesLookalikesAlone(t *testing.T) {
	cases := []string{
		"see task-123456789012345678901234 in the tracker",
		"the prefix is sk- and then the key",
		"KEY=short",
		"MONKEY=bananabanana",
		"DONKEY_KONG=junglebeats1",
		"SSH_AUTH_SOCK=/run/user/1000/keyring/ssh",
		"TOKEN=${VAULT_TOKEN}",
		"PASSWORD=changeme-now",
		"HOME=/home/someone",
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	for _, c := range cases {
		got, n := RedactSecrets(c)
		if n != 0 || got != c {
			t.Errorf("RedactSecrets(%q) = %q, %d; want it untouched", c, got, n)
		}
		if what := ScanSecret(c); what != "" {
			t.Errorf("ScanSecret(%q) = %q; want none", c, what)
		}
	}
}

// A whole `env` dump is what actually leaked. The values go, the names
// and the ordinary variables stay — a redacted dump the model can still
// reason about.
func TestRedactSecretsOnAnEnvDump(t *testing.T) {
	dump := strings.Join([]string{
		"SHELL=/bin/bash",
		"DEEPSEEK_API_KEY_PERSONAL=sk-dcaf42a37674b1e9c0d5a8f3b27e6104",
		"OPENROUTER_API_KEY=sk-or-v1-" + strings.Repeat("ab12", 16),
		"DASHSCOPE_API_KEY=sk-sp-6133bcaa91de40f7b8c2",
		"PWD=/home/someone/wspace/janetcode",
		"GITHUB_TOKEN=ghp_" + strings.Repeat("aB3d", 9),
		"LANG=en_US.UTF-8",
	}, "\n")
	got, n := RedactSecrets(dump)
	if n != 4 {
		t.Fatalf("redacted %d values, want 4:\n%s", n, got)
	}
	for _, keep := range []string{"SHELL=/bin/bash", "PWD=/home/someone/wspace/janetcode", "LANG=en_US.UTF-8", "DEEPSEEK_API_KEY_PERSONAL=", "GITHUB_TOKEN="} {
		if !strings.Contains(got, keep) {
			t.Errorf("lost %q from the dump:\n%s", keep, got)
		}
	}
	for _, gone := range []string{"sk-dcaf42a37674", "sk-or-v1-", "sk-sp-6133bc", "ghp_"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q survived redaction:\n%s", gone, got)
		}
	}
	if ScanSecret(got) != "" {
		t.Errorf("redacted dump still scans as %q", ScanSecret(got))
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

// RedactSecrets must agree with ScanSecret on what is a secret, blank
// only the span, and leave the surrounding evidence readable — a
// decision state whose failing output is replaced wholesale is a
// decision made blind.
func TestRedactSecretsBlanksTheSpanNotTheText(t *testing.T) {
	cases := []struct {
		in, want string
		n        int
	}{
		{"go test ./... failed: FAIL", "go test ./... failed: FAIL", 0},
		{"curl -H 'Authorization: Bearer sk-ant-api03-AbCdEfGhIjKlMnOpQrStUv' https://x",
			"curl -H 'Authorization: Bearer [redacted]' https://x", 1},
		{"api_key = AbCdEf0123456789AbCdEf0123456789 in .env",
			"api_key = [redacted] in .env", 1},
		{"api_key = your-key-here", "api_key = your-key-here", 0},
		{"two: ghp_AbCdEf0123456789AbCdEf0123456789 and AKIAIOSFODNN7EXAMPLE",
			"two: [redacted] and [redacted]", 2},
	}
	for _, c := range cases {
		got, n := RedactSecrets(c.in)
		if got != c.want || n != c.n {
			t.Errorf("RedactSecrets(%q) = %q, %d; want %q, %d", c.in, got, n, c.want, c.n)
		}
		// Whatever ScanSecret flags, RedactSecrets must remove.
		if (ScanSecret(c.in) != "") != (c.n > 0) {
			t.Errorf("ScanSecret disagrees with RedactSecrets on %q", c.in)
		}
		if ScanSecret(got) != "" {
			t.Errorf("redacted text still scans as %q: %q", ScanSecret(got), got)
		}
	}
}
