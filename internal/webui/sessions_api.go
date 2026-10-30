// Session management routes: rename, archive, star, delete, branch from a
// message and rewind to one. The sidebar's per-row actions call these; the
// live session is changed through its own handle, the others by id on disk.
package webui

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const turnRunning = "wait for the current turn to finish"

// sessionBody is every field the /api/session/* routes read.
type sessionBody struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Archived *bool    `json:"archived"`
	Pinned   *bool    `json:"pinned"`
	IDs      []string `json:"ids"`
	Entry    string   `json:"entry"`
	Files    bool     `json:"files"`
}

func (s *server) sessionOp(w http.ResponseWriter, r *http.Request) {
	op := strings.TrimPrefix(r.URL.Path, "/api/session/")
	if op == "rewind" && r.Method == http.MethodGet {
		s.rewindPreview(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var b sessionBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&b); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	switch op {
	case "rename":
		title := textfmt.Clip(strings.TrimSpace(b.Title), 80)
		if title == "" {
			http.Error(w, "a title is required", http.StatusBadRequest)
			return
		}
		s.metaReply(w, b.ID, func(m *session.Meta) { m.Title = title })
	case "archive":
		v := b.Archived == nil || *b.Archived
		s.metaReply(w, b.ID, func(m *session.Meta) { m.Archived = v })
	case "pin":
		s.pinOp(w, b)
	case "delete":
		s.deleteOp(w, b.ID)
	case "branch":
		s.branchOp(w, b)
	case "rewind":
		s.rewindOp(w, b)
	default:
		http.NotFound(w, r)
	}
}

// metaReply applies fn to session id and answers; the status is the one
// the failure maps to.
func (s *server) metaReply(w http.ResponseWriter, id string, fn func(*session.Meta)) {
	if code, err := s.changeMeta(id, fn); err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]string{"ok": "1", "id": id})
}

func (s *server) changeMeta(id string, fn func(*session.Meta)) (int, error) {
	if strings.TrimSpace(id) == "" {
		return http.StatusBadRequest, errors.New("a session id is required")
	}
	if cur := s.sess(); cur != nil && cur.Meta.ID == id {
		if err := cur.UpdateMeta(fn); err != nil {
			return http.StatusInternalServerError, err
		}
		return http.StatusOK, nil
	}
	dir, err := session.Dir()
	if err != nil {
		return http.StatusInternalServerError, err
	}
	err = session.ModifyMeta(dir, id, fn)
	var held session.ErrHeld
	switch {
	case err == nil:
		return http.StatusOK, nil
	case errors.As(err, &held):
		return http.StatusConflict, err
	case errors.Is(err, os.ErrNotExist):
		return http.StatusNotFound, errors.New("unknown session")
	}
	return http.StatusBadRequest, err
}

// pinOp stars or unstars one session, or with ids stars a batch: the
// one-time move of the pins the page used to keep in localStorage.
func (s *server) pinOp(w http.ResponseWriter, b sessionBody) {
	if len(b.IDs) > 0 {
		n := 0
		for _, id := range b.IDs {
			if code, err := s.changeMeta(id, func(m *session.Meta) { m.Starred = true }); err == nil && code == http.StatusOK {
				n++
			}
		}
		writeJSON(w, map[string]any{"ok": "1", "pinned": n})
		return
	}
	v := b.Pinned == nil || *b.Pinned
	s.metaReply(w, b.ID, func(m *session.Meta) { m.Starred = v })
}

func (s *server) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *server) deleteOp(w http.ResponseWriter, id string) {
	if strings.TrimSpace(id) == "" {
		http.Error(w, "a session id is required", http.StatusBadRequest)
		return
	}
	cur := s.sess()
	live := cur != nil && cur.Meta.ID == id
	if live && s.isRunning() {
		http.Error(w, turnRunning, http.StatusConflict)
		return
	}
	dir, err := session.Dir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if live {
		// The view moves to a fresh conversation first, then the old file
		// goes once its lease is let go.
		if err := s.startNewSession(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cur.Release()
	}
	err = session.Delete(dir, id)
	var held session.ErrHeld
	switch {
	case err == nil:
		writeJSON(w, map[string]string{"ok": "1", "id": id})
	case errors.As(err, &held):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "unknown session", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

// userOrdinal is which typed message (1-based) the thread entry id is, or
// belongs to: the nearest user entry at or before it. onUser says the
// entry is itself that message.
func (s *server) userOrdinal(id string) (n int, onUser, found bool, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Kind == "user" {
			n++
			text = e.Text
		}
		if e.ID == id {
			return n, e.Kind == "user", n > 0, text
		}
	}
	return 0, false, false, ""
}

func (s *server) branchOp(w http.ResponseWriter, b sessionBody) {
	if s.isRunning() {
		http.Error(w, turnRunning, http.StatusConflict)
		return
	}
	cur := s.sess()
	n, onUser, ok, _ := s.userOrdinal(b.Entry)
	if cur == nil || !ok {
		http.Error(w, "unknown message", http.StatusNotFound)
		return
	}
	where := session.CutEndTurn
	if onUser {
		where = session.CutAfter
	}
	cut, ok := session.MessageCut(cur.Events(), n, where)
	if !ok {
		http.Error(w, "that message can no longer be located in the conversation (it was compacted or rewound)", http.StatusConflict)
		return
	}
	dir, err := session.Dir()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ns, err := session.ForkAt(dir, cur, cut, "branch of "+cur.Label())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, code, err := s.openSession(ns.Meta.ID, false)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	writeJSON(w, map[string]string{"ok": "1", "session": id})
}

// rewindTurn is the turn of the n-th typed message and whether its files
// can still be put back.
func (s *server) rewindTurn(n int) (turn int, restorable bool) {
	cur, ag := s.sess(), s.opts.Agent
	if cur == nil || ag == nil {
		return 0, false
	}
	events := cur.Events()
	cut, ok := session.MessageCut(events, n, session.CutBefore)
	if !ok {
		return 0, false
	}
	for _, e := range events {
		if e.Seq == cut {
			turn = e.Turn
		}
	}
	for _, t := range ag.RestorableTurns() {
		if t == turn && turn > 0 {
			return turn, true
		}
	}
	return turn, false
}

func (s *server) rewindPreview(w http.ResponseWriter, r *http.Request) {
	n, onUser, ok, text := s.userOrdinal(r.URL.Query().Get("entry"))
	if !ok || !onUser {
		http.Error(w, "unknown message", http.StatusNotFound)
		return
	}
	turn, restorable := s.rewindTurn(n)
	writeJSON(w, map[string]any{"text": text, "turn": turn, "has_checkpoint": restorable})
}

func (s *server) rewindOp(w http.ResponseWriter, b sessionBody) {
	n, onUser, ok, text := s.userOrdinal(b.Entry)
	if !ok || !onUser {
		http.Error(w, "unknown message", http.StatusNotFound)
		return
	}
	ag := s.opts.Agent
	if ag == nil {
		http.Error(w, "no agent to rewind", http.StatusConflict)
		return
	}
	ctx, started := s.begin("rewinding")
	if !started {
		http.Error(w, turnRunning, http.StatusConflict)
		return
	}
	defer s.idle()
	restored := false
	if turn, can := s.rewindTurn(n); b.Files && can {
		if _, err := ag.UndoToTurn(ctx, s.cwd(), turn, false); err == nil {
			if _, err := ag.ApplyPendingUndo(ctx, true); err != nil {
				http.Error(w, "restoring files: "+err.Error(), http.StatusConflict)
				return
			}
			restored = true
		}
	}
	// A turn restore rewinds the conversation with the files; when it did
	// not (or files were not asked for) the cut is made here.
	if countTyped(ag.MessagesSnapshot()) >= n {
		if _, err := ag.RewindBeforeUser(n); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	s.resetView()
	s.replayHistory(ag.MessagesSnapshot(), modelsOf(s.sess().Events()))
	s.emitStatus()
	writeJSON(w, map[string]any{"ok": "1", "text": text, "restored": restored})
}

func countTyped(msgs []llm.Message) int {
	n := 0
	for _, m := range msgs {
		if session.RealUser(m) {
			n++
		}
	}
	return n
}
