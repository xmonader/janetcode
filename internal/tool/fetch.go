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
	"regexp"
	"strings"
	"time"
)

// FetchURL downloads a web page or API response for the model. HTML is
// reduced to readable text; other content is returned as is, truncated.
type FetchURL struct {
	// Client defaults to one with a 30s timeout.
	Client *http.Client
}

// MaxFetchBytes caps the response body read from the network.
const MaxFetchBytes = 1 << 20

func (FetchURL) Name() string { return "fetch_url" }
func (FetchURL) Description() string {
	return "Fetch a URL over HTTP(S) and return its content: HTML is converted to plain text, JSON and text are returned as is. Use it for documentation, APIs, and pages the user refers to. Output is truncated to a readable size."
}

// Mutating is true because a fetch sends data off the machine (the URL and
// anything encoded in it), so the user approves it like a command.
func (FetchURL) Mutating() bool { return true }

func (FetchURL) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"url":{"type":"string","description":"Absolute http or https URL"}},
"required":["url"],"additionalProperties":false}`)
}

func (t FetchURL) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be absolute http(s): %q", in.URL)
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "janetcode/1 (+https://github.com/xmonader/janetcode)")
	req.Header.Set("Accept", "text/html, application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil && !errors.Is(err, context.Canceled) {
		return "", fmt.Errorf("read %s: %w", u.Host, err)
	}
	truncated := len(body) > MaxFetchBytes
	if truncated {
		body = body[:MaxFetchBytes]
	}
	ct := resp.Header.Get("Content-Type")
	text := string(body)
	if strings.Contains(ct, "text/html") || (ct == "" && looksLikeHTML(text)) {
		text = htmlToText(text)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %d %s\n\n", u.String(), resp.StatusCode, strings.TrimSpace(strings.Split(ct, ";")[0]))
	sb.WriteString(Truncate(text))
	if truncated {
		sb.WriteString("\n[response truncated at 1 MB]")
	}
	if resp.StatusCode >= 400 {
		return sb.String(), fmt.Errorf("HTTP %d from %s", resp.StatusCode, u.Host)
	}
	return sb.String(), nil
}

func looksLikeHTML(s string) bool {
	head := strings.ToLower(s[:min(len(s), 512)])
	return strings.Contains(head, "<html") || strings.Contains(head, "<!doctype html")
}

var (
	reDrop = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b.*?</script\s*>`),
		regexp.MustCompile(`(?is)<style\b.*?</style\s*>`),
		regexp.MustCompile(`(?is)<noscript\b.*?</noscript\s*>`),
		regexp.MustCompile(`(?is)<svg\b.*?</svg\s*>`),
		regexp.MustCompile(`(?is)<head\b.*?</head\s*>`),
		regexp.MustCompile(`(?s)<!--.*?-->`),
	}
	reLi     = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reLiEnd  = regexp.MustCompile(`(?i)</li\s*>`)
	reBlock  = regexp.MustCompile(`(?i)</?(p|div|br|ul|ol|h[1-6]|tr|table|section|article|header|footer|pre|blockquote|dt|dd)\b[^>]*>`)
	reTag    = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces = regexp.MustCompile(`[ \t\r\f]+`)
	reBlank  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText strips markup while keeping paragraph and list boundaries.
func htmlToText(s string) string {
	for _, re := range reDrop {
		s = re.ReplaceAllString(s, "")
	}
	s = reLi.ReplaceAllString(s, "\n- ")
	s = reLiEnd.ReplaceAllString(s, "")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = unescape(s)
	s = reSpaces.ReplaceAllString(s, " ")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		lines = append(lines, strings.TrimSpace(l))
	}
	s = strings.Join(lines, "\n")
	s = reBlank.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// unescape decodes every HTML entity, then folds the non-breaking spaces
// pages use for layout into plain ones.
func unescape(s string) string { return strings.ReplaceAll(html.UnescapeString(s), "\u00a0", " ") }
