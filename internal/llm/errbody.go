package llm

import (
	"errors"
	"net/http"
	"regexp"
	"strings"
)

// maxErrorBody bounds an error message taken from an HTTP response body
// before it reaches the UI and the session transcript. Real API errors
// fit comfortably; debug-page floods and proxy echoes do not.
const maxErrorBody = 300

// MaxErrorBodyRead bounds how much of a failed response's body is read at
// all. The message is cut to maxErrorBody runes afterwards either way, so
// reading more than this only holds it in memory for no purpose.
const MaxErrorBodyRead = 1 << 20

// secretish matches the shapes a provider or a proxy in front of one
// actually returns a credential in: an OpenAI-style key, a bearer token
// echoed back, a JWT. Redaction is by shape because the body is whatever
// some gateway decided to write — there is no field to omit.
var secretish = []*regexp.Regexp{
	// sk-..., sk-proj-..., and the same shape under another prefix.
	regexp.MustCompile(`\b(?:sk|pk|rk|api)[-_][A-Za-z0-9](?:[A-Za-z0-9._-]{6,})`),
	// "Bearer abc123", "token: abc123", "api-key = abc123".
	regexp.MustCompile(`(?i)\b(bearer|token|api[-_ ]?key|secret|password)\b(["']?\s*[:=]?\s*["']?)([A-Za-z0-9._\-+/]{12,}={0,2})`),
	// A JWT, which always starts with the base64 of {"alg".
	regexp.MustCompile(`\beyJ[A-Za-z0-9._-]{16,}`),
}

// Redact masks anything in s shaped like a credential.
//
// Dropping the body wholesale on 401 and 403 was not enough: a
// LiteLLM- or OpenRouter-style proxy answers 400 or 500 with the upstream
// error nested inside — "Incorrect API key provided: sk-..." — and that
// landed in the UI and in the transcript that persists on disk.
func Redact(s string) string {
	if s == "" {
		return s
	}
	for i, re := range secretish {
		if i == 1 {
			s = re.ReplaceAllString(s, "$1$2[redacted]")
			continue
		}
		s = re.ReplaceAllString(s, "[redacted]")
	}
	return s
}

// BoundErrorMessage whitespace-collapses, redacts and caps a message at
// maxErrorBody runes.
func BoundErrorMessage(msg string) string {
	msg = Redact(strings.Join(strings.Fields(msg), " "))
	if r := []rune(msg); len(r) > maxErrorBody {
		msg = string(r[:maxErrorBody]) + "…"
	}
	return msg
}

// SafeErrorMessage sanitises a RAW response body before it becomes an
// error message. On 401/403 the body is dropped entirely — a
// misbehaving proxy can echo the request's Authorization header (the
// API key) into the response body, and the transcript that would carry
// it persists on disk. Structured API error messages do not carry that
// risk and go through BoundErrorMessage instead. An empty return means
// "say only the status": the caller substitutes http.StatusText.
func SafeErrorMessage(status int, body string) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ""
	}
	return BoundErrorMessage(body)
}

// SafeErrorDetail is the unabridged companion to SafeErrorMessage: the
// whole server text, redacted but not cut, for the things that have to
// read past the display cap — dialect learning, and classification.
// Dropped on 401/403 for the same reason the message is.
func SafeErrorDetail(status int, body string) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return ""
	}
	return Redact(strings.Join(strings.Fields(body), " "))
}

// DetailedError is an error that kept an unabridged copy of the text its
// Error() abridges for display.
type DetailedError interface {
	error
	// ErrorDetail is the whole server text, redacted but uncut.
	ErrorDetail() string
}

// ClassifyText is the text a classification decision should read.
//
// Error() is bounded to maxErrorBody runes for the UI and the transcript,
// and classification used to read that bounded copy: a 429 from a verbose
// gateway whose "insufficient_quota" sat past the cut was graded a plain
// rate limit, so it was retried against a dead account and its key got a
// 15-second breaker instead of a quota one. Display and judgement need
// different lengths of the same text.
func ClassifyText(err error) string {
	var de DetailedError
	if errors.As(err, &de) {
		if d := de.ErrorDetail(); d != "" {
			return d
		}
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
