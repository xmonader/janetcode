package memory

import (
	"fmt"
	"regexp"
	"strings"
)

// Memory is written to disk, indexed into every later system prompt, and
// often committed: it is the last place an API key should end up. A key
// reaches it the way keys reach everything — the model reads a .env or a
// failing command's output, decides it is worth remembering, and writes
// it down. The shapes below are the ones that are unmistakable, so a
// refusal is never a guess about ordinary prose.

var secretPatterns = []struct {
	name string
	re   *regexp.Regexp
}{
	{"an Anthropic API key", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"an OpenAI API key", regexp.MustCompile(`sk-(?:proj-|svcacct-)?[A-Za-z0-9_\-]{32,}`)},
	{"an OpenRouter API key", regexp.MustCompile(`sk-or-v1-[A-Za-z0-9]{32,}`)},
	{"a Google API key", regexp.MustCompile(`AIza[A-Za-z0-9_\-]{30,}`)},
	{"an AWS access key id", regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`)},
	{"a GitHub token", regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{30,}`)},
	{"a Slack token", regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}`)},
	{"a Stripe secret key", regexp.MustCompile(`\bsk_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{"a private key block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"a JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
}

// assignedSecret catches the shape rather than the vendor: a name that
// says secret, assigned a long opaque value.
var assignedSecret = regexp.MustCompile(`(?i)\b(?:api[_\- ]?key|secret[_\- ]?key|access[_\- ]?token|auth[_\- ]?token|password|passwd|client[_\- ]?secret)\b\s*[:=]\s*["']?([A-Za-z0-9_\-+/.]{20,})["']?`)

// placeholders are the values people write instead of a secret; a
// documented example is worth remembering, and refusing it teaches
// nothing.
var placeholders = []string{"xxx", "your", "example", "changeme", "redacted", "placeholder", "<", "...", "****", "dummy", "sample", "env.", "${"}

// ScanSecret reports what looks like a credential in text, or "".
func ScanSecret(text string) string {
	for _, p := range secretPatterns {
		if p.re.MatchString(text) {
			return p.name
		}
	}
	for _, m := range assignedSecret.FindAllStringSubmatch(text, -1) {
		if !placeholder(m[1]) && looksRandom(m[1]) {
			return "a secret assigned in " + firstWord(m[0])
		}
	}
	return ""
}

// RedactPlaceholder is what a redacted span is replaced with.
const RedactPlaceholder = "[redacted]"

// RedactSecrets rewrites every credential-shaped span in text with
// RedactPlaceholder and reports how many it replaced. It is the
// span-level counterpart of ScanSecret, for callers that must send the
// text somewhere rather than refuse it: blanking a whole field costs a
// decision its evidence, while blanking the match costs it the key.
//
// The same patterns decide, in the same order, so a text RedactSecrets
// leaves alone is exactly a text ScanSecret reports nothing for. For an
// assignment it redacts the value only, keeping the name that makes the
// surrounding line readable.
func RedactSecrets(text string) (string, int) {
	n := 0
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllStringFunc(text, func(string) string {
			n++
			return RedactPlaceholder
		})
	}
	var b strings.Builder
	last := 0
	for _, m := range assignedSecret.FindAllStringSubmatchIndex(text, -1) {
		vs, ve := m[2], m[3]
		if vs < 0 || placeholder(text[vs:ve]) || !looksRandom(text[vs:ve]) {
			continue
		}
		b.WriteString(text[last:vs])
		b.WriteString(RedactPlaceholder)
		last = ve
		n++
	}
	if last == 0 {
		return text, n
	}
	b.WriteString(text[last:])
	return b.String(), n
}

func placeholder(v string) bool {
	l := strings.ToLower(v)
	for _, p := range placeholders {
		if strings.Contains(l, p) {
			return true
		}
	}
	return false
}

// looksRandom is true for a value with enough variety to be a key rather
// than a word or a path: both cases and a digit, or plainly base64-ish.
func looksRandom(v string) bool {
	var upper, lower, digit int
	for _, r := range v {
		switch {
		case r >= 'A' && r <= 'Z':
			upper++
		case r >= 'a' && r <= 'z':
			lower++
		case r >= '0' && r <= '9':
			digit++
		}
	}
	return upper > 0 && lower > 0 && digit > 0
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, " :="); i > 0 {
		return s[:i]
	}
	return s
}

// ErrSecret is returned instead of writing an entry that holds a
// credential.
func errSecret(what string) error {
	return fmt.Errorf("this looks like %s and was not remembered: memory is written to disk and put into every later prompt, so it is the wrong place for a credential. Remember where the value lives (an environment variable, a secret manager) rather than the value", what)
}
