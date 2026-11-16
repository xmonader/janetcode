package extension

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/xmonader/janetcode/internal/secret"
)

const redacted = "[redacted]"

// sensitiveName matches the name of a flag, variable, header or query
// parameter that carries a credential.
var sensitiveName = regexp.MustCompile(`(?i)(key|token|secret|passw|pwd|auth|credential|bearer|cookie|signature|session)`)

// opaque matches a path segment that looks like a credential: long,
// unbroken, and mixing letters with digits.
var opaque = regexp.MustCompile(`^[A-Za-z0-9_\-]{24,}$`)

// RedactURL hides what in a URL can be a credential: the userinfo, the
// value of every query parameter whose name says credential, and a path
// segment that looks like a token (some servers carry the secret in the
// path). The host and the rest of the path stay, since they are what
// the user needs to recognise the server.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		out, _ := secret.Redact(raw)
		return out
	}
	if u.User != nil {
		u.User = url.User(redacted)
	}
	if q := u.Query(); len(q) > 0 {
		for k := range q {
			if sensitiveName.MatchString(k) {
				q[k] = []string{redacted}
			}
		}
		u.RawQuery = q.Encode()
	}
	segs := strings.Split(u.Path, "/")
	for i, s := range segs {
		if opaque.MatchString(s) && strings.ContainsAny(s, "0123456789") {
			segs[i] = redacted
		}
	}
	u.Path = strings.Join(segs, "/")
	u.RawPath = ""
	out := u.String()
	// url.Values.Encode and URL.String escape the marker's brackets.
	out = strings.NewReplacer("%5Bredacted%5D", redacted).Replace(out)
	out, _ = secret.Redact(out)
	return out
}

// RedactArgs hides credentials in a command line: --flag=value where
// the flag says credential, the word after a bare --flag that does, URLs
// inside an argument, and whatever the secret scanner recognises.
func RedactArgs(args []string) []string {
	out := make([]string, len(args))
	hideNext := false
	for i, a := range args {
		switch {
		case hideNext:
			out[i] = redacted
			hideNext = false
			continue
		case strings.HasPrefix(a, "-") && !strings.Contains(a, "="):
			out[i] = a
			hideNext = sensitiveName.MatchString(a)
			continue
		}
		if name, _, ok := strings.Cut(a, "="); ok && sensitiveName.MatchString(name) && !strings.Contains(name, "://") {
			out[i] = name + "=" + redacted
			continue
		}
		if strings.Contains(a, "://") {
			a = RedactURL(a)
		}
		out[i], _ = secret.Redact(a)
	}
	return out
}

// RedactText is for a free-form command line (a hook's command): the
// URL and assignment forms are hidden, then the scanner runs.
func RedactText(s string) string {
	s, _ = secret.Redact(s) // whole-line patterns (Authorization: Bearer ...) first
	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}
	return strings.Join(RedactArgs(words), " ")
}
