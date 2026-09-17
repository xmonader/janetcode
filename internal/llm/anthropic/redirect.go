package anthropic

import (
	"fmt"
	"net/http"
	"strings"
)

// newRedirectSafeClient gives c a redirect policy that drops x-api-key
// on a hop to another scheme or host. Go's default policy strips
// Authorization, Cookie and WWW-Authenticate cross-host but not custom
// auth headers — and x-api-key is the one this API authenticates with,
// so a mischievous redirect (an open redirector on the endpoint's host,
// a proxy that answers 302) would otherwise forward the key to
// whoever the Location names. A caller that chose its own policy keeps
// it.
func newRedirectSafeClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	if c.CheckRedirect != nil {
		return c
	}
	clone := *c
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		first := via[0].URL
		if !strings.EqualFold(req.URL.Scheme, first.Scheme) || !strings.EqualFold(req.URL.Host, first.Host) {
			req.Header.Del("X-Api-Key")
		}
		return nil
	}
	return &clone
}
