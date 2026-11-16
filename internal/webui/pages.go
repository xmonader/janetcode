package webui

import (
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

// Pages the model attached with render_html are HTML the model wrote, run
// in the reader's browser. They are served from their own route and framed
// by the page with sandbox="allow-scripts ..." and no allow-same-origin, so
// the frame has an opaque origin: its scripts cannot read the parent
// document, the API, the cookie or the token. The response also carries a
// CSP that forbids any request from the page to anything but https
// resources, and forbids forms and <base>.
//
// The route needs the same cookie every other route needs (the frame's
// navigation sends it); the token is never in the URL and never in the
// body. A page link is therefore useless to anyone without a session.

// pageCSP is what the page may do. Inline script and style are the point
// of a self-contained page; https: lets it load a charting library from a
// CDN. connect-src 'none' stops fetch/XHR/WebSocket/beacons, so a page can
// load resources but cannot read or post anything back.
const pageCSP = "sandbox allow-scripts allow-popups allow-popups-to-escape-sandbox; " +
	"default-src 'none'; " +
	"script-src 'unsafe-inline' https:; " +
	"style-src 'unsafe-inline' https:; " +
	"img-src data: blob: https:; " +
	"font-src data: https:; " +
	"media-src data: blob: https:; " +
	"connect-src 'none'; " +
	"form-action 'none'; " +
	"base-uri 'none'; " +
	"object-src 'none'; " +
	"frame-src 'none'; " +
	"frame-ancestors 'self' vscode-webview: vscode-file:"

// pageBridge runs first in the page. It sets the theme, follows the
// parent's theme changes, and makes every link open in a new tab: the
// frame is sandboxed with allow-popups-to-escape-sandbox, so the new tab
// is an ordinary, unsandboxed one with no opener.
const pageBridge = `<script>(function(){
var d=document.documentElement;
function set(t){if(t!=="dark"&&t!=="light")return;d.setAttribute("data-theme",t);d.style.colorScheme=t}
window.addEventListener("message",function(e){
if(e.source!==parent||!e.data||e.data.type!=="janetcode-theme")return;set(e.data.theme)});
document.addEventListener("click",function(e){
var a=e.target&&e.target.closest?e.target.closest("a[href]"):null;
if(!a||(a.getAttribute("href")||"").charAt(0)==="#")return;
a.setAttribute("target","_blank");a.setAttribute("rel","noopener noreferrer")},true);
function rep(){if(parent!==window)parent.postMessage({type:"janetcode-height",h:Math.ceil(d.getBoundingClientRect().height)},"*")}
if(window.ResizeObserver)new ResizeObserver(rep).observe(d);
window.addEventListener("load",rep);
})()</script>`

var (
	headOpenRE = regexp.MustCompile(`(?i)<head(\s[^>]*)?>`)
	htmlOpenRE = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	doctypeRE  = regexp.MustCompile(`(?is)^\s*<!doctype[^>]*>`)
)

// injectPageChrome puts the theme and the link bridge at the top of the
// document's head, wherever a head can legitimately go, without pushing a
// doctype out of first place (that would switch the page to quirks mode).
func injectPageChrome(doc, theme string) string {
	chrome := `<meta name="color-scheme" content="light dark"><style>:root{color-scheme:` + theme + `}</style>` + pageBridge +
		`<script>document.documentElement.setAttribute("data-theme",` + strconv.Quote(theme) + `)</script>`
	if loc := headOpenRE.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + chrome + doc[loc[1]:]
	}
	if loc := htmlOpenRE.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + "<head>" + chrome + "</head>" + doc[loc[1]:]
	}
	if loc := doctypeRE.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + "<head>" + chrome + "</head>" + doc[loc[1]:]
	}
	return "<!doctype html><head>" + chrome + "</head>" + doc
}

// pageAPI serves /page/<session id>/<n>?theme=light|dark.
func (s *server) pageAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	sid, num, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/page/"), "/")
	n, err := strconv.Atoi(num)
	if !ok || err != nil || n < 1 {
		http.NotFound(w, r)
		return
	}
	path, err := s.sessionPath(sid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, body, err := session.LoadPage(path, n)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot read page", http.StatusInternalServerError)
		return
	}
	theme := r.URL.Query().Get("theme")
	if theme != "dark" {
		theme = "light"
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	// guard sent X-Frame-Options: DENY for a non-loopback host; the page
	// is meant to be framed by this server's own UI, which frame-ancestors
	// above allows.
	h.Del("X-Frame-Options")
	_, _ = w.Write([]byte(injectPageChrome(string(body), theme)))
}

// sessionPath is the session file of id: the live session's own path, or
// a stored one under the sessions directory.
func (s *server) sessionPath(id string) (string, error) {
	if cur := s.sess(); cur != nil && cur.Meta.ID == id {
		return cur.Path(), nil
	}
	dir, err := session.Dir()
	if err != nil {
		return "", err
	}
	return session.PathFor(dir, id)
}
