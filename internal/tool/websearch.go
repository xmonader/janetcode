package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// SearchConfig selects the search backend, the "search" object in the
// config file.
type SearchConfig struct {
	// Provider is "duckduckgo" (default, no key: the HTML endpoint is
	// scraped) or "brave" (the Search API; needs a key).
	Provider string `json:"provider,omitempty"`
	// Key is the API key; "${BRAVE_API_KEY}" style references are expanded
	// from the environment.
	Key string `json:"key,omitempty"`
	// URL overrides the endpoint (a self-hosted proxy, or tests).
	URL string `json:"url,omitempty"`
}

// Validate rejects unknown providers and a brave provider without a key.
func (c SearchConfig) Validate() error {
	switch strings.ToLower(c.Provider) {
	case "", "duckduckgo", "ddg":
		return nil
	case "brave":
		if os.ExpandEnv(c.Key) == "" {
			return errors.New("search: brave needs a key (BRAVE_API_KEY)")
		}
		return nil
	}
	return fmt.Errorf("search: unknown provider %q (duckduckgo or brave)", c.Provider)
}

// WebSearch queries a search engine and returns titles, URLs and snippets
// so the model can pick a page to fetch.
type WebSearch struct {
	Config SearchConfig
	// Client defaults to one with a 20s timeout.
	Client *http.Client
}

// MaxSearchResults caps what one call returns.
const MaxSearchResults = 10

// SearchResult is one hit.
type SearchResult struct {
	Title, URL, Snippet string
}

func (WebSearch) Name() string { return "web_search" }
func (t WebSearch) Description() string {
	return "Search the web and return the top results with title, URL and snippet (via " + t.providerName() + "). Use it to find documentation, error messages, package names or recent information, then fetch_url the page you need. Prefer specific queries with exact identifiers."
}

// Mutating is true because the query leaves the machine.
func (WebSearch) Mutating() bool { return true }

func (WebSearch) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"query":{"type":"string","description":"Search terms; quote exact phrases"},
"max_results":{"type":"integer","description":"How many results to return, 1-10 (default 5)"}},
"required":["query"],"additionalProperties":false}`)
}

func (t WebSearch) providerName() string {
	if strings.ToLower(t.Config.Provider) == "brave" {
		return "Brave Search"
	}
	return "DuckDuckGo"
}

func (t WebSearch) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query      string `json:"query"`
		MaxResults int    `json:"max_results"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	in.Query = strings.TrimSpace(in.Query)
	if in.Query == "" {
		return "", errors.New("query is required")
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 5
	}
	if in.MaxResults > MaxSearchResults {
		in.MaxResults = MaxSearchResults
	}
	results, err := t.Search(ctx, in.Query, in.MaxResults)
	if err != nil {
		return "", err
	}
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q on %s. Try fewer or different words.", in.Query, t.providerName()), nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d result(s) for %q (%s):\n", len(results), in.Query, t.providerName())
	for i, r := range results {
		fmt.Fprintf(&sb, "\n%d. %s\n   %s\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", r.Snippet)
		}
	}
	return sb.String(), nil
}

// Search runs the query against the configured backend.
func (t WebSearch) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	if strings.ToLower(t.Config.Provider) == "brave" {
		return t.brave(ctx, client, query, n)
	}
	return t.duckduckgo(ctx, client, query, n)
}

func (t WebSearch) get(ctx context.Context, client *http.Client, target string, headers map[string]string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; janetcode/1; +https://github.com/xmonader/janetcode)")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%s refused the search (HTTP %d); wait a while or configure another provider under \"search\" in the config: %s", t.providerName(), resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("%s: HTTP %d: %s", t.providerName(), resp.StatusCode, msg)
	}
	return body, nil
}

// DuckDuckGo's HTML endpoint needs no key. Results are anchors with class
// result__a; links go through a redirect whose uddg parameter is the
// real URL.
const duckduckgoURL = "https://html.duckduckgo.com/html/"

func (t WebSearch) duckduckgo(ctx context.Context, client *http.Client, query string, n int) ([]SearchResult, error) {
	base := t.Config.URL
	if base == "" {
		base = duckduckgoURL
	}
	body, err := t.get(ctx, client, base+"?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	return parseDuckDuckGo(string(body), n), nil
}

var (
	reDDGResult  = regexp.MustCompile(`(?s)<a[^>]*class="result__a"[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reDDGSnippet = regexp.MustCompile(`(?s)<a[^>]*class="result__snippet"[^>]*>(.*?)</a>`)
)

func parseDuckDuckGo(page string, n int) []SearchResult {
	var out []SearchResult
	// Each result block holds the title anchor followed by its snippet;
	// walk the page in order so snippets pair with the right title.
	for len(out) < n {
		loc := reDDGResult.FindStringSubmatchIndex(page)
		if loc == nil {
			break
		}
		href := html.UnescapeString(page[loc[2]:loc[3]])
		title := strings.TrimSpace(htmlToText(page[loc[4]:loc[5]]))
		rest := page[loc[1]:]
		snippet := ""
		next := reDDGResult.FindStringIndex(rest)
		window := rest
		if next != nil {
			window = rest[:next[0]]
		}
		if sm := reDDGSnippet.FindStringSubmatch(window); sm != nil {
			snippet = strings.TrimSpace(htmlToText(sm[1]))
		}
		page = rest
		target := ddgTarget(href)
		if target == "" || title == "" {
			continue
		}
		out = append(out, SearchResult{Title: title, URL: target, Snippet: snippet})
	}
	return out
}

// ddgTarget unwraps //duckduckgo.com/l/?uddg=<url> redirects and drops ads.
func ddgTarget(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.HasSuffix(u.Host, "duckduckgo.com") {
		if strings.HasPrefix(u.Path, "/y.js") { // ad click tracker
			return ""
		}
		if real := u.Query().Get("uddg"); real != "" {
			return real
		}
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// Brave Search API: https://api.search.brave.com/res/v1/web/search
const braveURL = "https://api.search.brave.com/res/v1/web/search"

func (t WebSearch) brave(ctx context.Context, client *http.Client, query string, n int) ([]SearchResult, error) {
	key := os.ExpandEnv(t.Config.Key)
	if key == "" {
		return nil, errors.New("brave search needs a key: set \"search\": {\"provider\": \"brave\", \"key\": \"${BRAVE_API_KEY}\"} in the config")
	}
	base := t.Config.URL
	if base == "" {
		base = braveURL
	}
	q := url.Values{"q": {query}, "count": {fmt.Sprint(n)}, "text_decorations": {"0"}}
	body, err := t.get(ctx, client, base+"?"+q.Encode(), map[string]string{"Accept": "application/json", "X-Subscription-Token": key})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("brave search: bad reply: %w", err)
	}
	var out []SearchResult
	for _, r := range resp.Web.Results {
		if len(out) >= n {
			break
		}
		if r.URL == "" {
			continue
		}
		out = append(out, SearchResult{Title: strings.TrimSpace(htmlToText(r.Title)), URL: r.URL, Snippet: strings.TrimSpace(htmlToText(r.Description))})
	}
	return out, nil
}
