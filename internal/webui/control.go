package webui

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/sessionproto"
)

// Several clients can be attached to one session at once: two browsers, a
// phone, a script following along. This file is how the web server keeps
// them from stepping on each other, in the terms of sessionproto (the
// wire contract is docs/manual/protocol.md).
//
// A client is named by the X-Janet-Client header, the client query
// parameter (an EventSource cannot set headers), or the janet_client
// cookie the page is given, in that order; a request with none is the
// client "anonymous", so every unnamed script is one client. Whoever asks
// to change the session while nobody controls it becomes its controller;
// while a controller's lease is live, anyone else's change is refused
// with a 409 and the state the refused client should have acted on. An
// observer, which attached its event stream with role=observer, can never
// change anything. The lease is kept alive by the controller's open
// event stream and by its requests, so a controller that vanishes stops
// holding the session after the lease's TTL.

const (
	clientHeader   = "X-Janet-Client"
	clientNameHdr  = "X-Janet-Client-Name"
	protocolHeader = "X-Janet-Protocol"
	epochHeader    = "X-Janet-Epoch"
	clientCookie   = "janet_client"
	anonymous      = "anonymous"
)

// readOnlyPOST are the POST routes that change nothing in the session,
// so an observer may use them.
var readOnlyPOST = map[string]bool{
	"/api/mermaid": true, // renders a diagram
	"/api/control": true, // asks about control; checks for itself
}

// clientID is who sent r.
func clientID(r *http.Request) string {
	for _, v := range []string{
		r.Header.Get(clientHeader),
		r.URL.Query().Get("client"),
		cookieValue(r, clientCookie),
	} {
		if id := cleanClient(v); id != "" {
			return id
		}
	}
	return anonymous
}

func cookieValue(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

// cleanClient bounds a client id to something safe to echo in JSON and
// logs; anything else counts as no id.
func cleanClient(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 64 {
		return ""
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return ""
		}
	}
	return v
}

func clientName(r *http.Request) string {
	n := strings.TrimSpace(r.Header.Get(clientNameHdr))
	if n == "" {
		n = r.URL.Query().Get("name")
	}
	n = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return -1
		}
		return c
	}, n)
	if len(n) > 64 {
		n = strings.ToValidUTF8(n[:64], "")
	}
	return n
}

// giveClientCookie names the browser on its first page load, so a tab
// and the reloads of it are one client and another browser is another.
func giveClientCookie(w http.ResponseWriter, r *http.Request) {
	if cleanClient(cookieValue(r, clientCookie)) != "" {
		return
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: clientCookie, Value: "web-" + hex.EncodeToString(b), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 365 * 24 * 3600})
}

// busyQueued is whether a turn runs and how many prompts wait behind it.
func (s *server) busyQueued() (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running, len(s.queued)
}

// controlView is the control state of the session as client sees it.
func (s *server) controlView(client string) sessionproto.Snapshot {
	busy, queued := s.busyQueued()
	return s.ctl.View(client, busy, queued)
}

// writeConflict answers a refused request: 409 with the conflict, whose
// state is what the client should re-read before it tries again.
func writeConflict(w http.ResponseWriter, c *sessionproto.Conflict) {
	status := http.StatusConflict
	if c.Code == sessionproto.CodeBadVersion {
		status = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": c.Message, "conflict": c})
}

// protocolOK checks the version a client says it speaks.
func (s *server) protocolOK(r *http.Request) *sessionproto.Conflict {
	v := strings.TrimSpace(r.Header.Get(protocolHeader))
	if v == "" {
		return nil
	}
	if n, err := strconv.Atoi(v); err != nil || n != sessionproto.Version {
		return &sessionproto.Conflict{
			Code:    sessionproto.CodeBadVersion,
			Message: "this server speaks session protocol " + strconv.Itoa(sessionproto.Version) + ", not " + v,
			State:   s.controlView(clientID(r)),
		}
	}
	return nil
}

func isMutation(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/api/") && !readOnlyPOST[r.URL.Path]
}

// control is the gate in front of the routes: it checks the protocol
// version of every API request and the control lease of every change.
func (s *server) control(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/events" {
			w.Header().Set(protocolHeader, strconv.Itoa(sessionproto.Version))
			if c := s.protocolOK(r); c != nil {
				writeConflict(w, c)
				return
			}
		}
		if r.URL.Path == "/" {
			giveClientCookie(w, r)
		}
		if isMutation(r) {
			var epoch uint64
			if v := r.Header.Get(epochHeader); v != "" {
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					http.Error(w, "bad "+epochHeader, http.StatusBadRequest)
					return
				}
				epoch = n
			}
			busy, queued := s.busyQueued()
			if c := s.ctl.Authorize(clientID(r), clientName(r), epoch, busy, queued); c != nil {
				writeConflict(w, c)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// controlAPI is /api/control: GET reads the state, POST {"action":
// "acquire"|"take"|"release"} changes who controls the session.
func (s *server) controlAPI(w http.ResponseWriter, r *http.Request) {
	client := clientID(r)
	if r.Method == http.MethodGet {
		writeJSON(w, s.controlView(client))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	busy, queued := s.busyQueued()
	var (
		snap sessionproto.Snapshot
		c    *sessionproto.Conflict
	)
	switch body.Action {
	case "acquire":
		snap, c = s.ctl.Acquire(client, clientName(r), busy, queued)
	case "take":
		snap, c = s.ctl.Take(client, clientName(r), busy, queued)
	case "release":
		s.ctl.Release(client)
		snap = s.ctl.View(client, busy, queued)
	default:
		http.Error(w, `action must be "acquire", "take" or "release"`, http.StatusBadRequest)
		return
	}
	if c != nil {
		writeConflict(w, c)
		return
	}
	writeJSON(w, snap)
}

// controlTTL is how long a controller may go unheard from.
func controlTTL(o Options) time.Duration {
	if o.ControlTTL > 0 {
		return o.ControlTTL
	}
	return sessionproto.DefaultTTL
}
