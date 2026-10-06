package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The JSON search APIs: each takes a query and an API key (SearXNG takes
// an instance URL instead) and returns title, URL and a snippet under
// its own field names.
type apiBackend struct {
	label    string
	endpoint string // default; Config.URL overrides it
	keyEnv   string // where the key comes from when the config has none
	// request builds the call: method, URL, headers, JSON body (nil for
	// a GET).
	request func(base, key, query string, n int) (string, string, map[string]string, any)
	// parse reads the reply.
	parse func(body []byte) ([]SearchResult, error)
}

var apiBackends = map[string]apiBackend{
	// https://docs.tavily.com/documentation/api-reference/endpoint/search
	"tavily": {
		label: "Tavily", endpoint: "https://api.tavily.com/search", keyEnv: "TAVILY_API_KEY",
		request: func(base, key, q string, n int) (string, string, map[string]string, any) {
			return http.MethodPost, base, map[string]string{"Authorization": "Bearer " + key}, map[string]any{"query": q, "max_results": n}
		},
		parse: func(b []byte) ([]SearchResult, error) {
			var r struct {
				Results []struct{ Title, URL, Content string } `json:"results"`
			}
			err := json.Unmarshal(b, &r)
			var out []SearchResult
			for _, x := range r.Results {
				out = append(out, SearchResult{x.Title, x.URL, x.Content})
			}
			return out, err
		},
	},
	// https://serper.dev (Google results)
	"serper": {
		label: "Serper", endpoint: "https://google.serper.dev/search", keyEnv: "SERPER_API_KEY",
		request: func(base, key, q string, n int) (string, string, map[string]string, any) {
			return http.MethodPost, base, map[string]string{"X-API-KEY": key}, map[string]any{"q": q, "num": n}
		},
		parse: func(b []byte) ([]SearchResult, error) {
			var r struct {
				Organic []struct{ Title, Link, Snippet string } `json:"organic"`
			}
			err := json.Unmarshal(b, &r)
			var out []SearchResult
			for _, x := range r.Organic {
				out = append(out, SearchResult{x.Title, x.Link, x.Snippet})
			}
			return out, err
		},
	},
	// https://docs.exa.ai/reference/search
	"exa": {
		label: "Exa", endpoint: "https://api.exa.ai/search", keyEnv: "EXA_API_KEY",
		request: func(base, key, q string, n int) (string, string, map[string]string, any) {
			body := map[string]any{"query": q, "numResults": n, "contents": map[string]any{"text": map[string]any{"maxCharacters": 400}}}
			return http.MethodPost, base, map[string]string{"x-api-key": key}, body
		},
		parse: func(b []byte) ([]SearchResult, error) {
			var r struct {
				Results []struct{ Title, URL, Text string } `json:"results"`
			}
			err := json.Unmarshal(b, &r)
			var out []SearchResult
			for _, x := range r.Results {
				out = append(out, SearchResult{x.Title, x.URL, x.Text})
			}
			return out, err
		},
	},
	// A SearXNG instance with the json format enabled
	// (search.formats in its settings.yml).
	"searxng": {
		label: "SearXNG",
		request: func(base, _, q string, n int) (string, string, map[string]string, any) {
			return http.MethodGet, strings.TrimRight(base, "/") + "/search?" + url.Values{"q": {q}, "format": {"json"}}.Encode(), nil, nil
		},
		parse: func(b []byte) ([]SearchResult, error) {
			var r struct {
				Results []struct{ Title, URL, Content string } `json:"results"`
			}
			err := json.Unmarshal(b, &r)
			var out []SearchResult
			for _, x := range r.Results {
				out = append(out, SearchResult{x.Title, x.URL, x.Content})
			}
			return out, err
		},
	},
}

func (t WebSearch) api(ctx context.Context, client *http.Client, b apiBackend, query string, n int) ([]SearchResult, error) {
	base := t.Config.URL
	if base == "" {
		base = b.endpoint
	}
	if base == "" {
		return nil, fmt.Errorf("%s needs the instance URL: set \"search\": {\"provider\": %q, \"url\": \"http://localhost:8888\"}", b.label, strings.ToLower(b.label))
	}
	key := os.ExpandEnv(t.Config.Key)
	if key == "" && b.keyEnv != "" {
		key = os.Getenv(b.keyEnv)
	}
	if key == "" && b.keyEnv != "" {
		return nil, fmt.Errorf("%s search needs a key: set %s, or \"key\" under \"search\" in the config", b.label, b.keyEnv)
	}
	method, target, headers, body := b.request(base, key, query, n)
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Accept"] = "application/json"
	var raw []byte
	if body != nil {
		var err error
		if raw, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	if key != "" {
		client = keyGuard(client, b.label+" search")
	}
	reply, err := t.do(ctx, client, method, target, headers, raw)
	if err != nil {
		return nil, err
	}
	results, err := b.parse(reply)
	if err != nil {
		return nil, fmt.Errorf("%s search: bad reply: %w", b.label, err)
	}
	var out []SearchResult
	for _, r := range results {
		if len(out) >= n {
			break
		}
		if r.URL == "" {
			continue
		}
		out = append(out, SearchResult{Title: strings.TrimSpace(htmlToText(r.Title)), URL: r.URL, Snippet: clipSnippet(strings.TrimSpace(htmlToText(r.Snippet)))})
	}
	return out, nil
}

// clipSnippet keeps a page-text snippet (Tavily and Exa return a lot) to
// a few lines' worth.
func clipSnippet(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return s
}

// keyGuard refuses a redirect off the endpoint's host or down to http:
// Go forwards custom headers (X-API-KEY, x-api-key) to any host it is
// redirected to, stripping only Authorization and cookies, so following
// one would hand the key to whoever answers.
func keyGuard(client *http.Client, name string) *http.Client {
	guarded := *client
	inner := client.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if first := via[0].URL; !strings.EqualFold(first.Host, req.URL.Host) || (first.Scheme == "https" && req.URL.Scheme != "https") {
			return fmt.Errorf("%s: refusing a redirect from %s to %s: the request carries the API key", name, first.Scheme+"://"+first.Host, req.URL.Scheme+"://"+req.URL.Host)
		}
		if inner != nil {
			return inner(req, via)
		}
		return nil
	}
	return &guarded
}
