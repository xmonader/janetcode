package anthropic

import (
	"net/http"

	"github.com/xmonader/janetcode/internal/llm"
)

// newRedirectSafeClient gives c the shared provider redirect policy: a
// hop off the endpoint's own origin is refused rather than followed with
// the key stripped, because the request body carries the conversation
// even when the header no longer carries the key. See llm.SafeRedirectClient.
func newRedirectSafeClient(c *http.Client) *http.Client {
	return llm.SafeRedirectClient(c)
}
