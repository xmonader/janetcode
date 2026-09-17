package llm

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// SafeRedirectClient returns c with a redirect policy that refuses to
// follow a hop off the endpoint's own origin.
//
// A provider call carries two secrets: the API key in a header, and the
// whole conversation — system prompt, file contents, tool output — in the
// body. Go's default policy strips Authorization, Cookie and
// WWW-Authenticate when the host changes, but it counts a subdomain as
// the same site (api.example.com -> evil.api.example.com keeps the
// header), it never compares the scheme, and it knows nothing about a
// provider's own auth header — x-api-key is what Anthropic authenticates
// with, and a gateway's custom headers are forwarded unconditionally. It
// also always forwards the body, since 307 and 308 preserve the method
// and the payload.
//
// Stripping headers closes half of that. Refusing the hop closes both,
// and no provider legitimately redirects a POST to another origin. A
// same-host hop still works, including an http -> https upgrade.
//
// A caller that set its own CheckRedirect keeps it, but it now runs after
// this policy rather than instead of it: a client supplied through
// configuration must not be able to turn the protection off by accident.
func SafeRedirectClient(c *http.Client) *http.Client {
	if c == nil {
		c = &http.Client{}
	}
	clone := *c
	inner := c.CheckRedirect
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if first := via[0].URL; !sameOrigin(first, req.URL) {
			return fmt.Errorf("refusing a redirect from %s to %s: the request carries an API key and the whole conversation, and no provider redirects a call off its own origin", first.Scheme+"://"+first.Host, req.URL.Scheme+"://"+req.URL.Host)
		}
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &clone
}

// sameOrigin reports whether a hop stays on the endpoint's own host
// without weakening the transport. An http -> https upgrade is allowed;
// https -> http is not, because that is how a redirect turns a TLS
// request into a plaintext one.
func sameOrigin(from, to *url.URL) bool {
	if !strings.EqualFold(hostname(from), hostname(to)) {
		return false
	}
	if from.Port() != to.Port() {
		return false
	}
	f, t := strings.ToLower(from.Scheme), strings.ToLower(to.Scheme)
	return f == t || (f == "http" && t == "https")
}

// hostname is the host with a trailing dot removed: "api.x.com." and
// "api.x.com" name the same host to a resolver, and only one of them
// matches a naive string compare.
func hostname(u *url.URL) string {
	return strings.TrimSuffix(u.Hostname(), ".")
}
