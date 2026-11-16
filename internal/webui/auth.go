package webui

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// The server runs commands, so it requires a token, the way Jupyter does:
// Run prints a link that carries it, opening the link trades it for a
// cookie, and every request after that carries the cookie. Scripts can
// send it as "Authorization: Bearer <token>". Loopback is no exception:
// any other user or process on the machine can reach 127.0.0.1, and the
// origin and Host checks in guard only keep other web pages out.

// tokenEnv sets the token instead of a random one, for a URL that stays
// the same across restarts.
const tokenEnv = "JANETCODE_WEB_TOKEN"

// newToken returns the token from JANETCODE_WEB_TOKEN, or a random one.
func newToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv(tokenEnv)); t != "" {
		return t, nil
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// cookieName is per token: cookies are not separated by port, and two
// servers on one host must not overwrite each other's.
func cookieName(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "janetcode_" + hex.EncodeToString(sum[:4])
}

func (s *server) tokenMatches(got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(s.opts.Token)) == 1
}

// authorized reports whether r carries the token, as the cookie or as a
// bearer header.
func (s *server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName(s.opts.Token)); err == nil && s.tokenMatches(c.Value) {
		return true
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.tokenMatches(strings.TrimSpace(bearer))
}

// requireToken answers a request that must not go further and reports
// whether it did. The page itself, asked for with the right ?token=, sets
// the cookie and redirects to a URL without the token in it.
func (s *server) requireToken(w http.ResponseWriter, r *http.Request) bool {
	if s.opts.Token == "" {
		return false
	}
	if r.URL.Path == "/" && r.URL.Query().Has("token") {
		if !s.tokenMatches(r.URL.Query().Get("token")) {
			s.loginPage(w, "That token is not the one janetcode printed.")
			return true
		}
		s.signIn(w, r)
		return true
	}
	if r.URL.Path == "/" && r.URL.Query().Has("ticket") {
		if !s.tickets.redeem(r.URL.Query().Get("ticket")) {
			s.loginPage(w, "That link was already used or has expired. Open the link janetcode printed, or wait for the next notification.")
			return true
		}
		s.signIn(w, r)
		return true
	}
	if s.authorized(r) {
		return false
	}
	if r.URL.Path == "/" {
		s.loginPage(w, "")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "unauthorized: open the link janetcode printed, which carries the access token", http.StatusUnauthorized)
	return true
}

// signIn trades a good token or ticket for the cookie and redirects to
// the page without either in its URL. What the link was for survives the
// redirect: approve focuses the pending approval (the page reads it and
// takes it out of the address bar).
func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{
		Name: cookieName(s.opts.Token), Value: s.opts.Token, Path: "/",
		MaxAge: 30 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil,
	}
	if embedded(r) {
		// Framed by an editor's webview (editors/vscode): the page
		// is a cross-site frame there, and a Strict cookie is never
		// sent from one, so every call after this redirect would be
		// refused. Only on loopback, where the browser accepts a
		// Secure cookie over plain http; the Origin and Host gates
		// still stand behind it.
		c.SameSite, c.Secure = http.SameSiteNoneMode, true
	}
	http.SetCookie(w, c)
	keep := url.Values{}
	for _, k := range []string{"approve"} {
		if r.URL.Query().Has(k) {
			keep.Set(k, r.URL.Query().Get(k))
		}
	}
	to := "/"
	if len(keep) > 0 {
		to += "?" + keep.Encode()
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// embedded reports whether the page was asked for to be framed by an
// editor (?embed=1) on a loopback address.
func embedded(r *http.Request) bool {
	return r.URL.Query().Get("embed") == "1" && loopbackHost(r.Host)
}

// loginPage asks for the token when the page is opened without it.
func (s *server) loginPage(w http.ResponseWriter, problem string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	note := ""
	if problem != "" {
		note = `<p class="bad">` + html.EscapeString(problem) + `</p>`
	}
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>janetcode · sign in</title>
<style>
:root{color-scheme:light dark;--bg:#fbfbfa;--fg:#1d1d1b;--mute:#5d5d58;--bad:#b3261e;--line:#c9c9c4}
@media (prefers-color-scheme:dark){:root{--bg:#161615;--fg:#ececea;--mute:#a8a8a2;--bad:#f2b8b5;--line:#4a4a46}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif}
main{width:min(26rem,100vw - 32px)}h1{font-size:1.2rem;margin:0 0 .5rem}p{color:var(--mute);margin:.25rem 0 1rem}
.bad{color:var(--bad)}form{display:flex;gap:.5rem}input{flex:1;min-width:0;padding:.55rem .7rem;border:1px solid var(--line);border-radius:8px;background:transparent;color:inherit;font:inherit}
button{padding:.55rem 1rem;border:0;border-radius:8px;background:var(--fg);color:var(--bg);font:inherit;cursor:pointer}
</style></head><body><main><h1>This janetcode needs its access token</h1>
<p>It runs commands on this machine, so it asks for the token in the link it printed when it started. Open that link, or paste the token.</p>` + note + `
<form method="get" action="/"><input name="token" type="password" autocomplete="off" aria-label="Access token" placeholder="Access token" autofocus><button>Open</button></form>
</main></body></html>`))
}
