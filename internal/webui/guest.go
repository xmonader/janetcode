package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/session"
)

// Guest links let one other person follow the session in a browser and
// ask the agent things, with no account and no access to anything else.
//
// A guest link carries its own random token in the URL path
// (/guest/<token>/...). It is a different credential from the owner's
// access token and is checked by a different handler: guard sends every
// /guest/ path to guestHandler before the owner-token check, and
// guestHandler answers a fixed set of routes (the page, its script and
// style, the filtered event stream, and "say"). Nothing under /api/ or
// /events accepts a guest token, because the owner-token check there
// compares only against Options.Token, so the guest page cannot reach any
// owner endpoint however it is changed. The owner token never reaches the
// guest page: the page is a static file and the stream carries a
// whitelisted view of the transcript.
//
// What a guest sees is built by guestView from an allow-list of entry
// kinds and fields, not by removing what is sensitive from the owner's
// payloads, so a new owner-only field or event kind stays hidden until
// someone adds it here.

//go:embed guest
var guestFS embed.FS

const (
	// guestTTL is how long a link works.
	guestTTL = 24 * time.Hour
	// guestMaxText bounds one guest message, in bytes.
	guestMaxText = 8000
	// guestMaxPending bounds the requests waiting for the owner, so a
	// guest cannot flood the owner's screen or memory.
	guestMaxPending = 10
	// guestMaxStreams bounds the open event streams for one link.
	guestMaxStreams = 8
)

// guestLink is the one live guest link of a server. It is bound to the
// session it was made for and ends when it expires, is replaced or
// stopped, or the server leaves that session.
type guestLink struct {
	token     string
	sessionID string
	expires   time.Time
	// done is closed when the link ends, to hang up its open streams.
	done chan struct{}
	// bind is the secret the first browser to open the page was given in
	// a cookie; "" until then. Guarded by guestShare.mu.
	bind string
}

// guestRequest is a message a guest typed, waiting for the owner.
type guestRequest struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// guestPayload is what the owner's page shows about sharing.
type guestPayload struct {
	Active  bool      `json:"active"`
	Expires time.Time `json:"expires,omitzero"`
	// Path is the link's path, for the owner's "copy link": the token is
	// in it, so this payload goes to the owner's page only.
	Path     string         `json:"path,omitempty"`
	Requests []guestRequest `json:"requests"`
}

type guestShare struct {
	mu      sync.Mutex
	link    *guestLink
	reqs    []guestRequest
	nextID  int
	streams int
}

var (
	errNoGuestSession = errors.New("there is no saved session to share: a guest link follows one session, and this server is not recording one")
	errGuestEnded     = errors.New("this guest link has ended")
	errGuestFull      = errors.New("too many messages are waiting for the owner; wait for them to be sent or dismissed")
)

// endLocked ends the current link, if any, and drops what waited on it.
// Called with g.mu held.
func (g *guestShare) endLocked() {
	if g.link != nil {
		close(g.link.done)
		g.link = nil
	}
	g.reqs = nil
}

// createGuestLink makes a link for the current session and revokes the
// one before it. It returns the URL path and expiry.
func (s *server) createGuestLink() (string, time.Time, error) {
	sess := s.sess()
	if sess == nil {
		return "", time.Time{}, errNoGuestSession
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("making a guest token: %w", err)
	}
	l := &guestLink{
		token:     hex.EncodeToString(b),
		sessionID: sess.Meta.ID,
		expires:   time.Now().Add(guestTTL),
		done:      make(chan struct{}),
	}
	s.guest.mu.Lock()
	s.guest.endLocked()
	s.guest.link = l
	s.guest.mu.Unlock()
	s.emitGuest()
	return "/guest/" + l.token + "/", l.expires, nil
}

// stopGuestLink revokes the link at once and reports whether there was one.
func (s *server) stopGuestLink() bool {
	s.guest.mu.Lock()
	had := s.guest.link != nil
	s.guest.endLocked()
	s.guest.mu.Unlock()
	if had {
		s.emitGuest()
	}
	return had
}

// guestLinkFor returns the live link a token names, or nil: unknown,
// expired, stopped, or made for a session this server has left.
func (s *server) guestLinkFor(token string) *guestLink {
	s.guest.mu.Lock()
	l := s.guest.link
	s.guest.mu.Unlock()
	if l == nil || token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(l.token)) != 1 {
		return nil
	}
	if s.guestValid(l) {
		return l
	}
	return nil
}

// guestValid reports whether l is still the live link, within its time
// and on its session. An expired or orphaned link is ended here, so it
// needs no timer.
func (s *server) guestValid(l *guestLink) bool {
	s.guest.mu.Lock()
	cur := s.guest.link
	s.guest.mu.Unlock()
	if cur != l {
		return false
	}
	if time.Now().Before(l.expires) {
		if sess := s.sess(); sess != nil && sess.Meta.ID == l.sessionID {
			return true
		}
	}
	s.guest.mu.Lock()
	if s.guest.link == l {
		s.guest.endLocked()
	}
	s.guest.mu.Unlock()
	s.emitGuest()
	return false
}

// guestSessionChanged ends a link whose session the server just left.
func (s *server) guestSessionChanged(v *session.Session) {
	s.guest.mu.Lock()
	l := s.guest.link
	s.guest.mu.Unlock()
	if l != nil && (v == nil || v.Meta.ID != l.sessionID) {
		s.stopGuestLink()
	}
}

func (s *server) guestPayload() guestPayload {
	s.guest.mu.Lock()
	defer s.guest.mu.Unlock()
	p := guestPayload{Requests: append([]guestRequest{}, s.guest.reqs...)}
	if l := s.guest.link; l != nil && time.Now().Before(l.expires) {
		p.Active, p.Expires, p.Path = true, l.expires, "/guest/"+l.token+"/"
	}
	return p
}

// emitGuest tells every owner page what sharing looks like now. The
// guest stream drops this event type.
func (s *server) emitGuest() {
	p := s.guestPayload()
	s.emit(envelope{Type: "guest", Guest: &p})
}

// addGuestRequest queues a guest's message for the owner.
func (s *server) addGuestRequest(l *guestLink, text string) error {
	s.guest.mu.Lock()
	if s.guest.link != l {
		s.guest.mu.Unlock()
		return errGuestEnded
	}
	if len(s.guest.reqs) >= guestMaxPending {
		s.guest.mu.Unlock()
		return errGuestFull
	}
	s.guest.nextID++
	s.guest.reqs = append(s.guest.reqs, guestRequest{ID: "g" + strconv.Itoa(s.guest.nextID), Text: text, At: time.Now()})
	s.guest.mu.Unlock()
	s.emitGuest()
	return nil
}

// takeGuestRequest removes and returns a waiting request.
func (s *server) takeGuestRequest(id string) (guestRequest, bool) {
	s.guest.mu.Lock()
	defer s.guest.mu.Unlock()
	for i, r := range s.guest.reqs {
		if r.ID == id {
			s.guest.reqs = append(s.guest.reqs[:i:i], s.guest.reqs[i+1:]...)
			return r, true
		}
	}
	return guestRequest{}, false
}

// sendGuestRequest runs a waiting request as an ordinary turn under the
// owner's current permission mode.
func (s *server) sendGuestRequest(id string) error {
	r, ok := s.takeGuestRequest(id)
	if !ok {
		return fmt.Errorf("no waiting guest request %q", id)
	}
	s.emitGuest()
	// A guest's text is plain text for the model: not a slash command,
	// not a shell line, and no @path in it reads a file of the owner's.
	return s.submitPlain(neutralizeMentions(r.Text))
}

// dismissGuestRequest drops a waiting request.
func (s *server) dismissGuestRequest(id string) error {
	if _, ok := s.takeGuestRequest(id); !ok {
		return fmt.Errorf("no waiting guest request %q", id)
	}
	s.emitGuest()
	return nil
}

// neutralizeMentions puts a zero-width space after every "@" so
// prompt.ExpandMentions finds no path in text a guest wrote. The model
// still reads "@file" as typed.
func neutralizeMentions(text string) string {
	return strings.ReplaceAll(text, "@", "@\u200b")
}

// guestAPI is /api/guest, the owner's side of sharing. The guard has
// already required the owner's token; a guest token never gets here.
func (s *server) guestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, s.guestPayload())
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch body.Action {
	case "create":
		path, exp, err := s.createGuestLink()
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]any{"url": s.ownerBase() + path, "expires": exp})
	case "stop":
		s.stopGuestLink()
		writeJSON(w, map[string]string{"ok": "1"})
	case "send", "dismiss":
		var err error
		if body.Action == "send" {
			err = s.sendGuestRequest(body.ID)
		} else {
			err = s.dismissGuestRequest(body.ID)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, map[string]string{"ok": "1"})
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// cmdShare is /share: make a guest link for this session, or "stop" it.
func (s *server) cmdShare(arg string) (bool, error) {
	switch strings.ToLower(arg) {
	case "stop", "off":
		if s.stopGuestLink() {
			s.notice("guest link stopped")
		} else {
			s.notice("no guest link is active")
		}
	case "":
		path, exp, err := s.createGuestLink()
		if err != nil {
			s.errf("/share: %v", err)
			return false, nil
		}
		s.notice("guest link, a read-only view of this session until " + exp.Format("Mon 15:04") + " or /share stop: " + s.ownerBase() + path)
	default:
		s.errf("/share: usage /share | /share stop")
	}
	return false, nil
}

// ---- guest side ----

// guestHandler answers /guest/<token>/<route>. Every route checks the
// token first; an unknown, expired or stopped token is a 404 whatever
// the route, so a probe learns nothing.
func (s *server) guestHandler(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")

	rest := strings.TrimPrefix(r.URL.Path, "/guest/")
	token, route, hasSlash := strings.Cut(rest, "/")
	l := s.guestLinkFor(token)
	if l == nil {
		http.Error(w, "this guest link has ended or never existed", http.StatusNotFound)
		return
	}
	// The page is where a link gets bound to a browser; every other
	// route needs the cookie that binding set. The bare-path redirect
	// carries nothing, so it passes.
	opensPage := route == "" && hasSlash && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	if !(route == "" && !hasSlash) && !s.guestBrowser(w, r, l, opensPage) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, guestOtherBrowserPage)
		return
	}
	switch route {
	case "":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		if !hasSlash {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusSeeOther)
			return
		}
		s.guestFile(w, "index.html", "text/html; charset=utf-8")
	case "app.js":
		s.guestFile(w, "app.js", "text/javascript; charset=utf-8")
	case "app.css":
		s.guestFile(w, "app.css", "text/css; charset=utf-8")
	case "events":
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		if crossSite(r) {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		s.guestEvents(w, r, l)
	case "say":
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		if crossSite(r) {
			http.Error(w, "forbidden: cross-site request", http.StatusForbidden)
			return
		}
		s.guestSay(w, r, l)
	default:
		http.NotFound(w, r)
	}
}

const guestOtherBrowserPage = `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Link already opened</title>
<body style="font:16px system-ui,sans-serif;max-width:32em;margin:15vh auto;padding:0 1em"><h1>This link was already opened in another browser</h1>
<p>A guest link works in one browser. Ask the person who shared it to make you a new link.</p></body>`

// guestBrowser reports whether this request comes from the browser the
// link is bound to. The first browser to open the page (bindNow) is
// bound: it gets a cookie holding a random secret, scoped to this link's
// path, and every later request must present it. Compared in constant
// time; no cookie, or a wrong one, is another browser.
func (s *server) guestBrowser(w http.ResponseWriter, r *http.Request, l *guestLink, bindNow bool) bool {
	got := ""
	if c, err := r.Cookie(guestCookieName); err == nil {
		got = c.Value
	}
	s.guest.mu.Lock()
	defer s.guest.mu.Unlock()
	if l.bind == "" {
		if !bindNow {
			return false
		}
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return false
		}
		l.bind = hex.EncodeToString(b)
		http.SetCookie(w, &http.Cookie{
			Name: guestCookieName, Value: l.bind, Path: "/guest/" + l.token + "/",
			Expires: l.expires, HttpOnly: true, SameSite: http.SameSiteStrictMode,
			Secure: guestHTTPS(r),
		})
		return true
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(l.bind)) == 1
}

const guestCookieName = "janetcode_guest"

// guestHTTPS reports whether the guest reached the server over https:
// directly, or through a proxy on this machine that says so.
func guestHTTPS(r *http.Request) bool {
	return r.TLS != nil || (r.Header.Get("X-Forwarded-Proto") == "https" && loopbackAddr(r.RemoteAddr))
}

func (s *server) guestFile(w http.ResponseWriter, name, ctype string) {
	b, err := guestFS.ReadFile("guest/" + name)
	if err != nil {
		http.Error(w, "missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", ctype)
	_, _ = w.Write(b)
}

func (s *server) guestSay(w http.ResponseWriter, r *http.Request, l *guestLink) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4*guestMaxText)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(body.Text)
	switch {
	case text == "":
		http.Error(w, "empty", http.StatusBadRequest)
		return
	case len(text) > guestMaxText || !utf8.ValidString(text):
		http.Error(w, "message too long or not text", http.StatusRequestEntityTooLarge)
		return
	}
	if err := s.addGuestRequest(l, text); err != nil {
		code := http.StatusTooManyRequests
		if err == errGuestEnded {
			code = http.StatusNotFound
		}
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]string{"ok": "1"})
}

// guestEntryKinds are the transcript rows a guest sees. Diffs travel in
// a tool entry's Preview, cards and sheets in Data and their own kinds,
// and notices carry owner-side command output, so none of those pass.
var guestEntryKinds = map[string]bool{"user": true, "assistant": true, "thinking": true, "tool": true, "error": true}

// guestEntry copies the fields a guest may see: not Preview (the diff),
// Data, tone, token counts or run folding.
func guestEntry(e *entry) *entry {
	if e.Gone {
		return &entry{ID: e.ID, Gone: true}
	}
	if !guestEntryKinds[e.Kind] {
		return nil
	}
	return &entry{
		ID: e.ID, Kind: e.Kind, Text: e.Text, At: e.At, Done: e.Done, Model: e.Model,
		Name: e.Name, Summary: e.Summary, Output: e.Output, Dur: e.Dur,
		Error: e.Error, Denied: e.Denied, Running: e.Running,
	}
}

// guestView turns an owner envelope into what a guest may see, or false.
func guestView(e envelope) (envelope, bool) {
	switch e.Type {
	case "entry":
		if e.Entry == nil {
			return envelope{}, false
		}
		g := guestEntry(e.Entry)
		if g == nil {
			return envelope{}, false
		}
		return envelope{Seq: e.Seq, Type: "entry", Entry: g}, true
	case "status":
		if e.Status == nil {
			return envelope{}, false
		}
		return envelope{Type: "status", Status: &statusPayload{Running: e.Status.Running, Phase: e.Status.Phase, Elapsed: e.Status.Elapsed}}, true
	}
	return envelope{}, false
}

func (s *server) guestHello(l *guestLink) envelope {
	h := &helloPayload{}
	if s.opts.Agent != nil {
		h.Model = s.opts.Agent.Settings().Model
	}
	if sess := s.sess(); sess != nil && sess.Meta.ID == l.sessionID {
		h.Title = session.CleanTitle(sess.Title())
	}
	return envelope{Type: "hello", Hello: h}
}

func (s *server) guestEvents(w http.ResponseWriter, r *http.Request, l *guestLink) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	s.guest.mu.Lock()
	if s.guest.streams >= guestMaxStreams {
		s.guest.mu.Unlock()
		http.Error(w, "too many viewers on this link", http.StatusTooManyRequests)
		return
	}
	s.guest.streams++
	s.guest.mu.Unlock()
	defer func() {
		s.guest.mu.Lock()
		s.guest.streams--
		s.guest.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Connection", "keep-alive")
	ch := make(chan envelope, 64)
	// The snapshot and the subscription are taken under one lock so no
	// event falls between them.
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	ents := make([]entry, 0, len(s.entries))
	for _, e := range s.entries {
		ents = append(ents, *e)
	}
	st := s.statusOf(s.stateLocked())
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()

	writeSSE(w, s.guestHello(l))
	for i := range ents {
		if v, ok := guestView(envelope{Type: "entry", Entry: &ents[i]}); ok {
			writeSSE(w, v)
		}
	}
	if v, ok := guestView(envelope{Type: "status", Status: &st}); ok {
		writeSSE(w, v)
	}
	fl.Flush()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	end := func() {
		writeSSE(w, envelope{Type: "end"})
		fl.Flush()
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-l.done:
			end()
			return
		case e, ok := <-ch:
			if !ok {
				return // fell behind; the page reconnects and gets a new snapshot
			}
			if e.Type == "reset" {
				end()
				return
			}
			if v, ok := guestView(e); ok {
				writeSSE(w, v)
				fl.Flush()
			}
		case <-tick.C:
			if !s.guestValid(l) {
				end()
				return
			}
			stt := s.statusNow()
			if v, ok := guestView(envelope{Type: "status", Status: &stt}); ok {
				writeSSE(w, v)
				fl.Flush()
			}
		}
	}
}
