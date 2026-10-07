package mcp

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestOAuthDiscoveryLive runs discovery, with every check a login makes
// before the browser opens, against public MCP servers: the issuer and
// resource checks must not refuse servers that work. It needs the
// network, so it runs only with JANETCODE_OAUTH_LIVE=1; JANETCODE_OAUTH_LIVE_URLS
// (space-separated) replaces the default list.
func TestOAuthDiscoveryLive(t *testing.T) {
	if os.Getenv("JANETCODE_OAUTH_LIVE") == "" {
		t.Skip("set JANETCODE_OAUTH_LIVE=1 to run discovery against public MCP servers")
	}
	urls := strings.Fields(os.Getenv("JANETCODE_OAUTH_LIVE_URLS"))
	if len(urls) == 0 {
		urls = []string{
			"https://mcp.linear.app/mcp",
			"https://mcp.notion.com/mcp",
			"https://mcp.atlassian.com/v1/mcp",
			"https://api.githubcopilot.com/mcp/",
			"https://mcp.sentry.dev/mcp",
		}
	}
	client := &http.Client{Timeout: 20 * time.Second}
	for _, u := range urls {
		t.Run(u, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// What the server's 401 says, as a session would see it.
			hint := ""
			if req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(`{}`)); err == nil {
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Accept", "application/json, text/event-stream")
				if resp, err := client.Do(req); err == nil {
					hint = resourceMetadataHint(resp.Header.Get("WWW-Authenticate"))
					resp.Body.Close()
					t.Logf("HTTP %d · resource_metadata=%q", resp.StatusCode, hint)
				}
			}
			m, err := discover(ctx, client, u, hint, "")
			if err != nil {
				t.Fatalf("discovery refused a working server: %v", err)
			}
			for _, ep := range []struct{ what, url string }{{"authorization endpoint", m.AuthorizationEndpoint}, {"token endpoint", m.TokenEndpoint}} {
				if err := secureEndpoint(ep.what, ep.url); err != nil {
					t.Errorf("%v", err)
				}
			}
			if len(m.CodeChallengeMethods) > 0 && !contains(m.CodeChallengeMethods, "S256") {
				t.Errorf("no S256 in %v", m.CodeChallengeMethods)
			}
			t.Logf("issuer=%s fromDocument=%v iss_param=%v pkce=%v registration=%v", m.Issuer, m.fromDocument, m.IssParameter, m.CodeChallengeMethods, m.RegistrationEndpoint != "")
		})
	}
}
