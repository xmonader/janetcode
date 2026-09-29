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
	"regexp"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/weburl"
)

// FetchURL downloads a web page or API response for the model. HTML is
// reduced to readable text; other content is returned as is, truncated.
type FetchURL struct {
	// Client defaults to one with a 30s timeout.
	Client *http.Client
	// Typed holds the hosts the user named in their own messages. A
	// private-network target is fetched only when the user asked for it.
	Typed *weburl.Set
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
	target, rewritten := weburl.Normalize(in.URL)
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("url must be absolute http(s): %q", in.URL)
	}
	named := t.Typed.Has(u.Host)
	if why := weburl.PrivateTarget(u); why != "" && !named {
		return "", fmt.Errorf("refused: %s, and you were not asked to fetch it. Only a URL the user named themselves may point inside the local machine or network", why)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	// A redirect is another host's chance to be fetched under the approval
	// the user gave for this one, so a redirect off this host is handed
	// back instead of followed. A downgrade to http is refused on the same
	// grounds even when the host is unchanged: what was approved was a
	// request nobody on the path could read.
	guarded := *client
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		prev := via[len(via)-1].URL
		if req.URL.Host != prev.Host || (prev.Scheme == "https" && req.URL.Scheme != "https") {
			return http.ErrUseLastResponse
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	if !named {
		guarded.Transport = guardDial(client.Transport)
	}
	guarded.Transport = egress.Transport(guarded.Transport, egress.KindFetch)
	client = &guarded
	req.Header.Set("User-Agent", "janetcode/1 (+https://github.com/xmonader/janetcode)")
	req.Header.Set("Accept", "text/html, application/json, text/plain, */*")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode >= 300 && resp.StatusCode < 400 && loc != "" {
		next := loc
		if abs, aerr := u.Parse(loc); aerr == nil {
			next = abs.String()
		}
		return fmt.Sprintf("%s %d redirects to %s.\nThat is a different host, or drops from https to http, so it was not followed: the permission you have is for %s. Fetch the new URL explicitly if you want it.", u.String(), resp.StatusCode, next, u.Host), nil
	}
	// A read that stopped short — cancelled, or the connection dropped —
	// is not a page. Returning the bytes that did arrive gives the model
	// half a document with nothing to say it is half, and it answers from
	// it as if it had read the whole thing.
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFetchBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", u.Host, err)
	}
	truncated := len(body) > MaxFetchBytes
	if truncated {
		body = body[:MaxFetchBytes]
	}
	ct := resp.Header.Get("Content-Type")
	// A Content-Type is a claim by the server. The bytes are not: a tar
	// ball served as text/plain is still a tar ball, and putting it in the
	// context as text costs a great deal and says nothing.
	if kind := binaryKind(body); kind != "" {
		return fmt.Sprintf("%s %d: the body is %s (%d bytes), whatever its Content-Type of %q says. It is not text and was not read.", u.String(), resp.StatusCode, kind, len(body), strings.TrimSpace(strings.Split(ct, ";")[0])), nil
	}
	text := string(body)
	if strings.Contains(ct, "text/html") || looksLikeHTML(text) {
		text = htmlToText(text)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %d %s\n\n", u.String(), resp.StatusCode, strings.TrimSpace(strings.Split(ct, ";")[0]))
	if rewritten {
		fmt.Fprintf(&sb, "[%s is the page around the file; this is the file itself]\n\n", in.URL)
	}
	sb.WriteString(EscapeDelimiters(textfmt.Truncate(text)))
	if truncated {
		sb.WriteString("\n[response truncated at 1 MB]")
	}
	if resp.StatusCode >= 400 {
		return sb.String(), fmt.Errorf("HTTP %d from %s", resp.StatusCode, u.Host)
	}
	return sb.String(), nil
}

// guardDial is weburl.GuardDial for a one-shot fetch, which owns its
// transport and keeps no idle connection behind.
func guardDial(rt http.RoundTripper) http.RoundTripper { return weburl.GuardDial(rt, false) }

// refusePrivateDial is weburl.RefusePrivateDial.
var refusePrivateDial = weburl.RefusePrivateDial

// binaryMagic is the leading bytes of formats a fetch should never try to
// read as text, with what to call them.
var binaryMagic = []struct {
	prefix []byte
	name   string
}{
	{[]byte("\x89PNG\r\n\x1a\n"), "a PNG image"},
	{[]byte("GIF8"), "a GIF image"},
	{[]byte("\xff\xd8\xff"), "a JPEG image"},
	{[]byte("%PDF-"), "a PDF"},
	{[]byte("PK\x03\x04"), "a zip archive"},
	{[]byte("\x1f\x8b"), "gzip-compressed data"},
	{[]byte("\x7fELF"), "an ELF binary"},
	{[]byte("BZh"), "bzip2-compressed data"},
	{[]byte("\xfd7zXZ\x00"), "xz-compressed data"},
	{[]byte("RIFF"), "a RIFF container (audio or video)"},
	{[]byte("\x00\x00\x01\x00"), "an icon"},
}

// binaryKind names the format of a body that is not text, or "".
func binaryKind(body []byte) string {
	for _, m := range binaryMagic {
		if bytes.HasPrefix(body, m.prefix) {
			return m.name
		}
	}
	n := len(body)
	if n > 512 {
		n = 512
	}
	if bytes.IndexByte(body[:n], 0) >= 0 {
		return "binary data"
	}
	return ""
}

// Preview implements Previewer: the URL that will actually be fetched,
// which is not always the one that was asked for.
func (t FetchURL) Preview(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	if target, rewritten := weburl.Normalize(in.URL); rewritten {
		return "GET " + target + "\n(rewritten from " + in.URL + ": the raw file rather than the page around it)", nil
	}
	return "", nil
}

// Warn implements Warner.
func (t FetchURL) Warn(input json.RawMessage) string {
	var in struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	target, _ := weburl.Normalize(in.URL)
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || u.Host == "" {
		return ""
	}
	if why := weburl.PrivateTarget(u); why != "" {
		return why
	}
	if u.Scheme == "http" {
		return "http, not https: the request and the reply are readable in transit"
	}
	return ""
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
	reLi    = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reLiEnd = regexp.MustCompile(`(?i)</li\s*>`)
	reBlock = regexp.MustCompile(`(?i)</?(p|div|br|ul|ol|h[1-6]|tr|table|section|article|header|footer|pre|blockquote|dt|dd)\b[^>]*>`)
	// A table cell is a value of its own; stripped bare, "<td>go</td><td>1.22</td>"
	// reads "go1.22".
	reCell   = regexp.MustCompile(`(?i)</?(td|th)\b[^>]*>`)
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
	s = reCell.ReplaceAllString(s, " ")
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
