package tool

import (
	"bytes"
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

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/weburl"
)

// WebSearch queries a search engine and returns titles, URLs and snippets
// so the model can pick a page to fetch.
type WebSearch struct {
	Config weburl.SearchConfig
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
	if b, ok := apiBackends[strings.ToLower(t.Config.Provider)]; ok {
		return b.label
	}
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
		// Titles, URLs and snippets are page content: a result can carry
		// harness-shaped tags (a page written to inject agents), and
		// they must not parse as the harness speaking.
		fmt.Fprintf(&sb, "\n%d. %s\n   %s\n", i+1, EscapeDelimiters(r.Title), EscapeDelimiters(r.URL))
		if r.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", EscapeDelimiters(r.Snippet))
		}
	}
	return sb.String(), nil
}

// Search runs the query against the configured backend.
// Host is the host a search contacts: the configured endpoint's, or the
// provider's own. A permission rule's host scope reads it (see
// perm.RegisterHosts), so *(host:*.duckduckgo.com) in a deny rule turns
// searching off the way it would a fetch.
func (t WebSearch) Host() string {
	base := t.Config.URL
	if base == "" {
		base = duckduckgoURL
		if strings.ToLower(t.Config.Provider) == "brave" {
			base = braveURL
		}
	}
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Host)
}

func (t WebSearch) Search(ctx context.Context, query string, n int) ([]SearchResult, error) {
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	recorded := *client
	recorded.Transport = egress.Transport(client.Transport, egress.KindSearch)
	client = &recorded
	if strings.ToLower(t.Config.Provider) == "brave" {
		return t.brave(ctx, client, query, n)
	}
	if b, ok := apiBackends[strings.ToLower(t.Config.Provider)]; ok {
		return t.api(ctx, client, b, query, n)
	}
	return t.duckduckgo(ctx, client, query, n)
}

func (t WebSearch) get(ctx context.Context, client *http.Client, target string, headers map[string]string) ([]byte, error) {
	return t.do(ctx, client, http.MethodGet, target, headers, nil)
}

// do sends one request; a non-nil body is sent as JSON.
func (t WebSearch) do(ctx context.Context, client *http.Client, method, target string, headers map[string]string, body []byte) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
	out, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		msg := textfmt.ClipBytes(strings.TrimSpace(string(out)), 200)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("%s refused the search (HTTP %d); wait a while or configure another provider under \"search\" in the config: %s", t.providerName(), resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("%s: HTTP %d: %s", t.providerName(), resp.StatusCode, msg)
	}
	return out, nil
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
		key = os.Getenv("BRAVE_API_KEY")
	}
	if key == "" {
		return nil, errors.New("brave search needs a key: set \"search\": {\"provider\": \"brave\", \"key\": \"${BRAVE_API_KEY}\"} in the config")
	}
	base := t.Config.URL
	if base == "" {
		base = braveURL
	}
	client = keyGuard(client, "brave search")
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
