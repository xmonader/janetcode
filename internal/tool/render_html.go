package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Limits on a rendered page. The HTML the model writes is capped so a
// runaway call cannot fill the session directory; images embedded from
// the workspace are capped separately because a screenshot is a legitimate
// megabyte that the model did not write.
const (
	MaxPageHTMLBytes  = 2 << 20
	maxPageImageBytes = 4 << 20
	maxPageImagesAll  = 8 << 20
)

// RenderHTML attaches a self-contained HTML page to the current turn. The
// web UI shows it above the reply in a sandboxed frame; the terminal and
// headless runs show a one-line card with the saved file's path, so the
// page is usable everywhere.
type RenderHTML struct {
	Root string
	WS   *Workspace
	// Save stores the finished page beside the session and returns its
	// number and the path of the HTML file. It is the session's storage,
	// injected so this package does not depend on it.
	Save func(title string, html []byte) (n int, path string, err error)
}

func (RenderHTML) Name() string { return "render_html" }
func (RenderHTML) Description() string {
	return "Attach a visual page to your reply: a chart, table, diagram, image collage or UI mockup, as ONE self-contained HTML document " +
		"(inline CSS and JS; public CDN scripts and styles are allowed). Use it when a picture shows the answer better than text; " +
		"do not use it for prose or code. The page runs sandboxed with no access to the app, the network of the user's machine, or the workspace. " +
		"The page is not given a theme to follow automatically: style with `color-scheme: light dark` and light-dark() colours, or `[data-theme=\"dark\"]` rules; " +
		"the viewer sets html[data-theme] to light or dark. Local images in the workspace may be referenced by path (src=\"charts/a.png\") and are embedded; " +
		"paths outside the workspace are refused. Keep the written reply short: state the takeaway, not what the page contains. Max 2 MiB."
}
func (RenderHTML) Mutating() bool { return false }
func (RenderHTML) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"title":{"type":"string","description":"Short title shown above the page"},
"html":{"type":"string","description":"A complete self-contained HTML document"}},
"required":["title","html"],"additionalProperties":false}`)
}

func (t RenderHTML) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Title string `json:"title"`
		HTML  string `json:"html"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	title := strings.Join(strings.Fields(in.Title), " ")
	if title == "" {
		return "", errors.New("title is required")
	}
	if strings.TrimSpace(in.HTML) == "" {
		return "", errors.New("html is required")
	}
	if len(in.HTML) > MaxPageHTMLBytes {
		return "", fmt.Errorf("html is %d bytes; the limit is %d (2 MiB). Trim it, or load libraries from a CDN instead of inlining them", len(in.HTML), MaxPageHTMLBytes)
	}
	if t.Save == nil {
		return "", errors.New("render_html has no session to store the page in")
	}
	page, err := t.embedImages(ctx, in.HTML)
	if err != nil {
		return "", err
	}
	n, path, err := t.Save(title, []byte(page))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Page saved at %s (page %d), titled %q. It is shown above your reply in the web UI and the person can open the file in a browser. "+
		"Do not repeat its contents in text; state the takeaway.", path, n, title), nil
}

var (
	pageAttrRE = regexp.MustCompile(`(?i)(\b(?:src|poster)\s*=\s*)("([^"]*)"|'([^']*)')`)
	pageURLRE  = regexp.MustCompile(`(?i)url\(\s*("([^"]*)"|'([^']*)'|([^)"'\s]+))\s*\)`)
)

var imageMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".svg": "image/svg+xml", ".avif": "image/avif", ".bmp": "image/bmp",
	".ico": "image/x-icon",
}

// localRef reports whether ref names a file on disk rather than a URL, and
// returns the path with any file:// prefix dropped.
func localRef(ref string) (string, bool) {
	ref = strings.TrimSpace(ref)
	low := strings.ToLower(ref)
	switch {
	case ref == "", strings.HasPrefix(ref, "#"), strings.HasPrefix(ref, "//"):
		return "", false
	case strings.Contains(ref, "${"), strings.Contains(ref, "{{"), strings.Contains(ref, "<%"):
		// A template placeholder: filled in by the page's own script.
		return "", false
	case strings.HasPrefix(low, "file://"):
		return strings.TrimPrefix(ref, ref[:7]), true
	}
	// Any other scheme (https:, data:, blob:, javascript:) is not a path.
	if i := strings.Index(ref, ":"); i > 1 && !strings.ContainsAny(ref[:i], `/\`) {
		return "", false
	}
	return ref, true
}

// embedImages replaces local image references with data: URIs, so the page
// keeps working after the files move. A reference outside the workspace, or
// to something that is not an image, fails the whole call: a page with a
// silently missing picture is worse than an error the model can fix.
func (t RenderHTML) embedImages(ctx context.Context, page string) (string, error) {
	cache := map[string]string{}
	total := 0
	var firstErr error
	embed := func(ref string) string {
		p, ok := localRef(ref)
		if !ok || firstErr != nil {
			return ""
		}
		if uri, hit := cache[p]; hit {
			return uri
		}
		uri, size, err := t.imageURI(ctx, p)
		if err != nil {
			firstErr = fmt.Errorf("image %q: %w", ref, err)
			return ""
		}
		if total += size; total > maxPageImagesAll {
			firstErr = fmt.Errorf("embedded images total over %d MiB; use fewer or smaller images", maxPageImagesAll>>20)
			return ""
		}
		cache[p] = uri
		return uri
	}
	out := pageAttrRE.ReplaceAllStringFunc(page, func(m string) string {
		sub := pageAttrRE.FindStringSubmatch(m)
		ref := sub[3]
		if sub[2][0] == '\'' {
			ref = sub[4]
		}
		if uri := embed(ref); uri != "" {
			return sub[1] + `"` + uri + `"`
		}
		return m
	})
	out = pageURLRE.ReplaceAllStringFunc(out, func(m string) string {
		sub := pageURLRE.FindStringSubmatch(m)
		ref := sub[2] + sub[3] + sub[4]
		if uri := embed(ref); uri != "" {
			return `url("` + uri + `")`
		}
		return m
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

func (t RenderHTML) imageURI(ctx context.Context, p string) (string, int, error) {
	mime, ok := imageMIME[strings.ToLower(filepath.Ext(p))]
	if !ok {
		return "", 0, errors.New("only png, jpg, gif, webp, svg, avif, bmp and ico files can be embedded; inline other assets or use a CDN URL")
	}
	abs, err := resolveOn(t.WS, t.Root, p)
	if err != nil {
		return "", 0, fmt.Errorf("refused: %w (only files inside the workspace can be embedded)", err)
	}
	data, err := readFileOf(ctx, t.WS, abs)
	if err != nil {
		return "", 0, err
	}
	if len(data) > maxPageImageBytes {
		return "", 0, fmt.Errorf("is %d bytes; the per-image limit is %d (4 MiB)", len(data), maxPageImageBytes)
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), len(data), nil
}

// PageResultN reads the page number out of a render_html result, so a
// transcript rebuilt from the session can find the page again.
func PageResultN(output string) (int, bool) {
	i := strings.Index(output, "(page ")
	if i < 0 {
		return 0, false
	}
	rest := output[i+len("(page "):]
	j := strings.IndexByte(rest, ')')
	if j < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:j])
	return n, err == nil && n > 0
}

// PageResultLine is the one-line card a terminal shows for a result: the
// title and where the saved page is.
func PageResultLine(output string) (string, bool) {
	const mark = "Page saved at "
	rest, ok := strings.CutPrefix(output, mark)
	if !ok {
		return "", false
	}
	path, rest, ok := strings.Cut(rest, " (page ")
	if !ok {
		return "", false
	}
	_, rest, ok = strings.Cut(rest, "), titled ")
	if !ok {
		return "", false
	}
	quoted, _, _ := strings.Cut(rest, ". It is shown")
	title, err := strconv.Unquote(quoted)
	if err != nil {
		title = quoted
	}
	return "▣ " + title + " · " + path, true
}

// PageResultTitle is the title a render_html result was saved under.
func PageResultTitle(output string) string {
	line, ok := PageResultLine(output)
	if !ok {
		return ""
	}
	t, _, _ := strings.Cut(strings.TrimPrefix(line, "▣ "), " · ")
	return t
}

// RenderCall implements Renderer.
func (RenderHTML) RenderCall(json.RawMessage, RenderContext) []string { return nil }

// RenderResult implements Renderer: a one-line card with the title and the
// saved file's path.
func (RenderHTML) RenderResult(_ json.RawMessage, out string, _ RenderContext) []string {
	if line, ok := PageResultLine(out); ok {
		return []string{line}
	}
	return nil
}
