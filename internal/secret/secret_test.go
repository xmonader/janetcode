package secret

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
		if got := Scan(c); got == "" {
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
		if got := Scan(c); got != "" {
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
		got, n := Redact(c.in)
		if got != c.want || n != c.n {
			t.Errorf("Redact(%q) = %q, %d; want %q, %d", c.in, got, n, c.want, c.n)
		}
		if Scan(c.in) == "" {
			t.Errorf("Scan missed %q", c.in)
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
		got, n := Redact(c)
		if n != 0 || got != c {
			t.Errorf("Redact(%q) = %q, %d; want it untouched", c, got, n)
		}
		if what := Scan(c); what != "" {
			t.Errorf("Scan(%q) = %q; want none", c, what)
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
	got, n := Redact(dump)
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
	if Scan(got) != "" {
		t.Errorf("redacted dump still scans as %q", Scan(got))
	}
}

// Shapes the net used to let through. The vendor tokens are the current
// formats of hosts whose older formats were already caught (a GitHub
// fine-grained token, a GitLab personal token, Slack's refresh token);
// the rest are the ordinary places a key sits that the span patterns
// only knew in one spelling — a name in JSON quotes, a header in a JSON
// headers map, a password in a URL, the AWS secret in ~/.aws/credentials.
func TestRedactSecretsCatchesCommonPlacements(t *testing.T) {
	cases := []struct{ in, want string }{
		{"token github_pat_11ABCDEFG0" + strings.Repeat("aB3d", 14), "token [redacted]"},
		{"GITLAB=glpat-" + strings.Repeat("aB3d", 5), "GITLAB=[redacted]"},
		{"xoxe-1-" + strings.Repeat("aB3d", 5), "[redacted]"},
		{`{"api_key":"AbCdEf0123456789AbCdEf0123456789"}`, `{"api_key":"[redacted]"}`},
		{`{\"apiKey\": \"AbCdEf0123456789AbCdEf0123456789\"}`, `{\"apiKey\": \"[redacted]\"}`},
		{`"headers": {"Authorization": "Bearer 9f3a2b7c1d4e5f6071"}`, `"headers": {"Authorization": "Bearer [redacted]"}`},
		{`{"x-api-key": "9f3a2b7c1d4e5f60"}`, `{"x-api-key": "[redacted]"}`},
		{"origin\thttps://deploy:S3cretPassw0rd@git.example.org/o/r.git (fetch)", "origin\thttps://deploy:[redacted]@git.example.org/o/r.git (fetch)"},
		{"postgres://app:hunter2hunter2@db:5432/app", "postgres://app:[redacted]@db:5432/app"},
		{"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYzzzzzzzKEY", "aws_secret_access_key = [redacted]"},
	}
	for _, c := range cases {
		got, n := Redact(c.in)
		if got != c.want || n != 1 {
			t.Errorf("Redact(%q) = %q, %d; want %q, 1", c.in, got, n, c.want)
		}
		if Scan(c.in) == "" {
			t.Errorf("Scan missed %q", c.in)
		}
	}
	// A URL's userinfo is often not a secret at all: a documented local
	// database, a user name alone, a variable, a placeholder.
	for _, c := range []string{
		"postgres://postgres:postgres@localhost:5432/db",
		"ssh://git@github.com/o/r.git",
		"https://user:${TOKEN}@example.com",
		"mysql://root:<password>@127.0.0.1/app",
	} {
		if got, n := Redact(c); n != 0 {
			t.Errorf("Redact(%q) = %q; want it untouched", c, got)
		}
	}
}

// Redact must agree with Scan on what is a secret, blank
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
		got, n := Redact(c.in)
		if got != c.want || n != c.n {
			t.Errorf("Redact(%q) = %q, %d; want %q, %d", c.in, got, n, c.want, c.n)
		}
		// Whatever Scan flags, Redact must remove.
		if (Scan(c.in) != "") != (c.n > 0) {
			t.Errorf("Scan disagrees with Redact on %q", c.in)
		}
		if Scan(got) != "" {
			t.Errorf("redacted text still scans as %q: %q", Scan(got), got)
		}
	}
}

// A private key is its body, not its header. Only the BEGIN line was
// matched, so Redact replaced that line and sent the base64 key material
// on as it was; and an encrypted or DSA key's header was not matched at
// all.
func TestRedactRemovesTheWholePrivateKey(t *testing.T) {
	body := "MIIEowIBAAKCAQEAu1SU1LfVLPHCozMxH2Mo4lgOEePzNm0tRgeLezV6ffAt0gun"
	for _, kind := range []string{"RSA ", "OPENSSH ", "EC ", "DSA ", "ENCRYPTED ", ""} {
		in := "cat key.pem\n-----BEGIN " + kind + "PRIVATE KEY-----\n" + body + "\n" + body + "\n-----END " + kind + "PRIVATE KEY-----\nexit 0"
		got, n := Redact(in)
		if strings.Contains(got, body) || n == 0 {
			t.Errorf("Redact left the %skey material: %q", kind, got)
		}
		if !strings.HasPrefix(got, "cat key.pem\n") || !strings.HasSuffix(got, "\nexit 0") {
			t.Errorf("Redact took more than the %skey: %q", kind, got)
		}
	}
	// A key cut off before its END line (truncated output) is still a key.
	if got, _ := Redact("-----BEGIN RSA PRIVATE KEY-----\n" + body); strings.Contains(got, body) {
		t.Errorf("Redact left a truncated key's material: %q", got)
	}
}
