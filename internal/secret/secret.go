package secret

import (
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
	{"a GitHub token", regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{40,}`)},
	{"a GitLab token", regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`)},
	{"a Slack token", regexp.MustCompile(`\bxox[abceprs]-[A-Za-z0-9\-]{10,}`)},
	{"a Stripe secret key", regexp.MustCompile(`\bsk_(?:live|test)_[A-Za-z0-9]{16,}`)},
	{"a private key block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |PGP )?PRIVATE KEY-----`)},
	{"a JSON web token", regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}\.[A-Za-z0-9_\-]{10,}`)},
}

// genericKey is the vendor-agnostic form of the list above: sk- and then
// enough opaque characters that it cannot be prose. Every provider that
// copied OpenAI's prefix lands here, including the ones whose middle
// segment nobody has seen yet (sk-sp-…, sk-fw-…). The \b matters: without
// it `task-1234…` is an sk- key.
var genericKey = regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{20,}`)

// spanPattern is a shape where the name around the value is worth
// keeping and only the value is the credential. group is the submatch
// holding the value; ok is the extra test that keeps the net from
// catching ordinary text.
type spanPattern struct {
	name  func(m []string) string
	re    *regexp.Regexp
	group int
	ok    func(m []string) bool
}

// assignedSecret catches the shape rather than the vendor: a name that
// says secret, assigned a long opaque value.
//
// The name may be quoted and the quotes escaped — `"api_key": "…"` in a
// config file, `\"apiKey\":\"…\"` in JSON printed inside JSON — which is
// where a key most often sits; `quoted` is that optional closing quote.
var assignedSecret = regexp.MustCompile(`(?i)\b(?:api[_\- ]?key|secret[_\- ]?key|access[_\- ]?token|auth[_\- ]?token|password|passwd|client[_\- ]?secret|(?:aws_)?secret_access_key)\b` + quoted + `\s*[:=]\s*` + quoted + `([A-Za-z0-9_\-+/.]{20,})["']?`)

// quoted is an optional quote, JSON-escaped or not.
const quoted = `(?:\\?["'])?`

// envAssign is one shell variable assignment: the shape a whole `env`
// dump is made of. Whether it holds a credential is decided by the
// name's words, not by how the value looks — a password is short and
// pronounceable and still a password.
var envAssign = regexp.MustCompile(`(?m)(?:^|[\s;&|(])(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)=("[^"\n]{8,}?"|'[^'\n]{8,}?'|[^\s"'\n;&|]{8,})`)

// authHeader and apiKeyHeader are the two ways a key travels in a
// request the model pasted into a shell, or a headers map in a JSON
// config wrote down.
var authHeader = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization` + quoted + `\s*:\s*` + quoted + `(?:bearer|token|basic)\s+["']?([A-Za-z0-9_\-.=+/]{8,})`)
var apiKeyHeader = regexp.MustCompile(`(?i)\b(?:x-api-key|x-auth-token|x-access-token|api-key)` + quoted + `\s*:\s*` + quoted + `([^\s"'\\\n]{8,})`)

// urlPassword is the password in a URL's userinfo — a git remote with a
// token in it, a DATABASE_URL — which `git remote -v` and `env` print.
// The user name stays; ok keeps the documented local database
// (postgres:postgres) and anything else without a digit in it readable.
var urlPassword = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.\-]*://([^\s:/@]+):([^\s:/@]+)@`)

func urlPasswordOK(m []string) bool {
	user, pw := m[1], m[2]
	if pw == user {
		return false
	}
	var letter, digit bool
	for _, r := range pw {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			letter = true
		}
	}
	return (letter && digit) || len(pw) >= 16
}

// spanPatterns run after secretPatterns, in this order.
var spanPatterns = []spanPattern{
	{func(m []string) string { return "a secret assigned in " + firstWord(m[0]) }, assignedSecret, 1, func(m []string) bool { return looksRandom(m[1]) }},
	{func(m []string) string { return "a credential in " + m[1] }, envAssign, 2, func(m []string) bool { return secretVarName(m[1]) && !pathValue(m[2]) }},
	{func([]string) string { return "a bearer token" }, authHeader, 1, nil},
	{func([]string) string { return "an API key header" }, apiKeyHeader, 1, nil},
	{func([]string) string { return "a password in a URL" }, urlPassword, 2, urlPasswordOK},
}

// secretWords are the words that make a variable name a credential's
// name. The match is on whole `_`-separated words, so DEEPSEEK_API_KEY_PERSONAL
// is one and MONKEY is not.
var secretWords = map[string]bool{
	"KEY": true, "KEYS": true, "APIKEY": true, "TOKEN": true, "SECRET": true,
	"PASSWORD": true, "PASSWD": true, "CREDENTIAL": true, "CREDENTIALS": true, "AUTH": true,
}

func secretVarName(name string) bool {
	for _, w := range strings.Split(strings.ToUpper(name), "_") {
		if secretWords[w] {
			return true
		}
	}
	return false
}

// pathValue keeps SSH_AUTH_SOCK and its relatives readable: a filesystem
// path is where a credential lives, not the credential.
func pathValue(v string) bool {
	v = strings.Trim(v, `"'`)
	return strings.HasPrefix(v, "/") || strings.HasPrefix(v, "./") || strings.HasPrefix(v, "~/")
}

// placeholders are the values people write instead of a secret; a
// documented example is worth remembering, and refusing it teaches
// nothing.
var placeholders = []string{"xxx", "your", "example", "changeme", "redacted", "placeholder", "<", "...", "****", "dummy", "sample", "env.", "${"}

// Scan reports what looks like a credential in text, or "".
func Scan(text string) string {
	_, _, what := redactSecrets(text)
	return what
}

// Placeholder is what a redacted span is replaced with.
const Placeholder = "[redacted]"

// Redact rewrites every credential-shaped span in text with
// Placeholder and reports how many it replaced. It is the
// span-level counterpart of Scan, for callers that must send the
// text somewhere rather than refuse it: blanking a whole field costs a
// decision its evidence, while blanking the match costs it the key.
//
// The same patterns decide, in the same order, so a text Redact
// leaves alone is exactly a text Scan reports nothing for. For an
// assignment it redacts the value only, keeping the name that makes the
// surrounding line readable.
func Redact(text string) (string, int) {
	out, n, _ := redactSecrets(text)
	return out, n
}

// redactSecrets is the one pass both entry points read: it rewrites the
// text, counts the spans and names the first one. Sharing it is what
// makes "Scan finds nothing" and "Redact changes nothing"
// the same statement rather than two lists that drift apart.
func redactSecrets(text string) (string, int, string) {
	n, what := 0, ""
	for _, p := range secretPatterns {
		text = p.re.ReplaceAllStringFunc(text, func(string) string {
			n++
			if what == "" {
				what = p.name
			}
			return Placeholder
		})
	}
	text = genericKey.ReplaceAllStringFunc(text, func(m string) string {
		if placeholder(m) {
			return m
		}
		n++
		if what == "" {
			what = "an API key"
		}
		return Placeholder
	})
	for _, p := range spanPatterns {
		var b strings.Builder
		last := 0
		for _, idx := range p.re.FindAllStringSubmatchIndex(text, -1) {
			vs, ve := idx[2*p.group], idx[2*p.group+1]
			if vs < 0 {
				continue
			}
			m := groups(text, idx)
			if placeholder(m[p.group]) || (p.ok != nil && !p.ok(m)) {
				continue
			}
			b.WriteString(text[last:vs])
			b.WriteString(Placeholder)
			last = ve
			n++
			if what == "" {
				what = p.name(m)
			}
		}
		if last > 0 {
			b.WriteString(text[last:])
			text = b.String()
		}
	}
	return text, n, what
}

// groups materialises one match's submatches from its index pairs, so a
// pattern's ok and name can read the name beside the value.
func groups(text string, idx []int) []string {
	m := make([]string, len(idx)/2)
	for i := range m {
		if idx[2*i] >= 0 {
			m[i] = text[idx[2*i]:idx[2*i+1]]
		}
	}
	return m
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
