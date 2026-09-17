package llm

import (
	"net/http"
	"strings"
)

// maxErrorBody bounds an error message taken from an HTTP response body
// before it reaches the UI and the session transcript. Real API errors
// fit comfortably; debug-page floods and proxy echoes do not.
const maxErrorBody = 300

// BoundErrorMessage whitespace-collapses and caps a message at
// maxErrorBody runes.
func BoundErrorMessage(msg string) string {
	msg = strings.Join(strings.Fields(msg), " ")
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
