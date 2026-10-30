package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// sessionEnv points the session store at the test's own directories and
// returns a server whose live session is cur, with two stored others.
func sessionEnv(t *testing.T) (s *server, dir, root string, cur, a, b *session.Session) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	root = t.TempDir()
	var err error
	if dir, err = session.Dir(); err != nil {
		t.Fatal(err)
	}
	mk := func(text string) *session.Session {
		x, err := session.New(dir, root, "fake", "m")
		if err != nil {
			t.Fatal(err)
		}
		m := llm.TextMessage(llm.RoleUser, text)
		if _, err := x.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Turn: 1, Message: &m}); err != nil {
			t.Fatal(err)
		}
		return x
	}
	a, b, cur = mk("first"), mk("second"), mk("live")
	ag := &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(), Model: "m", Log: cur}
	s = newServer(t.Context(), Options{Agent: ag, Session: cur, Cwd: root, Version: "test"})
	return
}

func sessionRows(t *testing.T, s *server) map[string]map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, r := range rows {
		out[r["id"].(string)] = r
	}
	return out
}

func TestSessionManageRoutes(t *testing.T) {
	s, dir, _, cur, a, b := sessionEnv(t)
	h := s.handler()
	enc := func(v any) string { j, _ := json.Marshal(v); return string(j) }
	cases := []struct {
		name string
		path string
		body string
		want int
		then func(t *testing.T)
	}{
		{"rename other", "/api/session/rename", enc(map[string]any{"id": a.Meta.ID, "title": "  Renamed  "}), 200, func(t *testing.T) {
			if r := sessionRows(t, s)[a.Meta.ID]; r["title"] != "Renamed" {
				t.Fatalf("row = %v", r)
			}
		}},
		{"rename live", "/api/session/rename", enc(map[string]any{"id": cur.Meta.ID, "title": "Mine"}), 200, func(t *testing.T) {
			if cur.Title() != "Mine" {
				t.Fatalf("title = %q", cur.Title())
			}
		}},
		{"rename empty", "/api/session/rename", enc(map[string]any{"id": a.Meta.ID, "title": " "}), 400, nil},
		{"rename unknown", "/api/session/rename", enc(map[string]any{"id": "20000101-000000-nope", "title": "x"}), 404, nil},
		{"rename bad id", "/api/session/rename", enc(map[string]any{"id": "../x", "title": "x"}), 400, nil},
		{"archive", "/api/session/archive", enc(map[string]any{"id": a.Meta.ID}), 200, func(t *testing.T) {
			if sessionRows(t, s)[a.Meta.ID]["archived"] != true {
				t.Fatal("not archived")
			}
		}},
		{"unarchive", "/api/session/archive", enc(map[string]any{"id": a.Meta.ID, "archived": false}), 200, func(t *testing.T) {
			if sessionRows(t, s)[a.Meta.ID]["archived"] != false {
				t.Fatal("still archived")
			}
		}},
		{"pin", "/api/session/pin", enc(map[string]any{"id": a.Meta.ID}), 200, func(t *testing.T) {
			if sessionRows(t, s)[a.Meta.ID]["starred"] != true {
				t.Fatal("not starred")
			}
		}},
		{"unpin", "/api/session/pin", enc(map[string]any{"id": a.Meta.ID, "pinned": false}), 200, func(t *testing.T) {
			if sessionRows(t, s)[a.Meta.ID]["starred"] != false {
				t.Fatal("still starred")
			}
		}},
		{"migrate local pins", "/api/session/pin", enc(map[string]any{"ids": []string{a.Meta.ID, b.Meta.ID, "20000101-000000-gone"}}), 200, func(t *testing.T) {
			rows := sessionRows(t, s)
			if rows[a.Meta.ID]["starred"] != true || rows[b.Meta.ID]["starred"] != true {
				t.Fatalf("rows = %v", rows)
			}
		}},
		{"bad json", "/api/session/pin", "{", 400, nil},
		{"unknown op", "/api/session/explode", "{}", 404, nil},
		{"delete other", "/api/session/delete", enc(map[string]any{"id": b.Meta.ID}), 200, func(t *testing.T) {
			if _, err := os.Stat(filepath.Join(dir, b.Meta.ID+".jsonl")); !os.IsNotExist(err) {
				t.Fatalf("stat = %v", err)
			}
		}},
		{"delete unknown", "/api/session/delete", enc(map[string]any{"id": b.Meta.ID}), 404, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := postJSON(t, h, c.path, c.body)
			if w.Code != c.want {
				t.Fatalf("%s = %d %s, want %d", c.path, w.Code, w.Body, c.want)
			}
			if c.then != nil {
				c.then(t)
			}
		})
	}
	if w := httptest.NewRecorder(); true {
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/session/rename", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET rename = %d", w.Code)
		}
	}
}

func TestSessionDeleteLive(t *testing.T) {
	s, dir, _, cur, _, _ := sessionEnv(t)
	h := s.handler()
	body := `{"id":"` + cur.Meta.ID + `"}`
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if w := postJSON(t, h, "/api/session/delete", body); w.Code != http.StatusConflict {
		t.Fatalf("delete while a turn runs = %d %s, want 409", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, cur.Meta.ID+".jsonl")); err != nil {
		t.Fatalf("the live session was removed: %v", err)
	}
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
	if w := postJSON(t, h, "/api/session/delete", body); w.Code != http.StatusOK {
		t.Fatalf("delete idle live = %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(dir, cur.Meta.ID+".jsonl")); !os.IsNotExist(err) {
		t.Fatalf("live session file still there: %v", err)
	}
	if s.sess().Meta.ID == cur.Meta.ID {
		t.Fatal("the view stayed on the deleted session")
	}
}

func TestSessionRowRunning(t *testing.T) {
	s, _, _, cur, a, _ := sessionEnv(t)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	rows := sessionRows(t, s)
	if rows[cur.Meta.ID]["running"] != true || rows[a.Meta.ID]["running"] != false {
		t.Fatalf("running flags = %v / %v", rows[cur.Meta.ID]["running"], rows[a.Meta.ID]["running"])
	}
}

// chatEnv is a live session holding two exchanges, with the thread view
// and the agent's messages in step with it.
func chatEnv(t *testing.T) (s *server, ids []string, cur *session.Session) {
	t.Helper()
	s, _, _, cur, _, _ = sessionEnv(t)
	cur2, err := session.New(filepath.Dir(cur.Path()), s.cwd(), "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	cur = cur2
	turn := 0
	for _, x := range []struct {
		role llm.Role
		text string
	}{{llm.RoleUser, "one"}, {llm.RoleAssistant, "answer one"}, {llm.RoleUser, "two"}, {llm.RoleAssistant, "answer two"}} {
		if x.role == llm.RoleUser {
			turn++
		}
		m := llm.TextMessage(x.role, x.text)
		typ := session.TypeUserMessage
		if x.role == llm.RoleAssistant {
			typ = session.TypeAssistant
		}
		if _, err := cur.Append(session.Event{Type: typ, Surface: true, Turn: turn, Message: &m, Model: "model-" + x.text}); err != nil {
			t.Fatal(err)
		}
	}
	ag := &agent.Agent{Provider: &scripted{}, Tools: tool.NewRegistry(), Model: "m", Log: cur}
	ag.Messages = session.FoldSurface(cur.Events())
	s.opts.Agent, s.opts.Session = ag, cur
	s.resetView()
	for _, m := range ag.Messages {
		kind := "assistant"
		if m.Role == llm.RoleUser {
			kind = "user"
		}
		ids = append(ids, s.add(entry{Kind: kind, Text: m.Text(), Done: true}))
	}
	return s, ids, cur
}

func TestSessionBranchFromMessage(t *testing.T) {
	cases := []struct {
		name  string
		entry int // index into ids
		want  int // messages in the branch
	}{
		{"first user message", 0, 1},
		{"first answer", 1, 2},
		{"second user message", 2, 3},
		{"last answer", 3, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, ids, cur := chatEnv(t)
			w := postJSON(t, s.handler(), "/api/session/branch", `{"entry":"`+ids[c.entry]+`"}`)
			if w.Code != http.StatusOK {
				t.Fatalf("branch = %d %s", w.Code, w.Body)
			}
			var out map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			dir, _ := session.Dir()
			_, msgs, err := session.Open(dir, out["session"])
			if err != nil || len(msgs) != c.want {
				t.Fatalf("branch holds %d messages (%v), want %d", len(msgs), err, c.want)
			}
			if got := s.sess().Meta; got.ID != out["session"] || got.ForkOf != cur.Meta.ID {
				t.Fatalf("view is on %+v", got)
			}
		})
	}
	s, _, _ := chatEnv(t)
	if w := postJSON(t, s.handler(), "/api/session/branch", `{"entry":"nope"}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown entry = %d", w.Code)
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if w := postJSON(t, s.handler(), "/api/session/branch", `{"entry":"e1"}`); w.Code != http.StatusConflict {
		t.Errorf("branch while running = %d", w.Code)
	}
}

func TestSessionRewindTruncatesHistory(t *testing.T) {
	s, ids, _ := chatEnv(t)
	h := s.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/session/rewind?entry="+ids[2], nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"two"`) || !strings.Contains(w.Body.String(), `"has_checkpoint":false`) {
		t.Fatalf("preview = %d %s", w.Code, w.Body)
	}
	// An answer is not a message to edit.
	if w := postJSON(t, h, "/api/session/rewind", `{"entry":"`+ids[1]+`"}`); w.Code != http.StatusNotFound {
		t.Fatalf("rewind to an answer = %d", w.Code)
	}
	w = postJSON(t, h, "/api/session/rewind", `{"entry":"`+ids[2]+`"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"text":"two"`) {
		t.Fatalf("rewind = %d %s", w.Code, w.Body)
	}
	var texts []string
	for _, m := range s.opts.Agent.MessagesSnapshot() {
		texts = append(texts, m.Text())
	}
	if len(texts) != 3 || texts[0] != "one" || texts[1] != "answer one" || !strings.Contains(texts[2], "[harness]") {
		t.Fatalf("history after rewind = %q", texts)
	}
	for _, x := range texts {
		if x == "two" || x == "answer two" {
			t.Fatalf("history still holds %q", x)
		}
	}
	// What a resume folds from the log is the same history.
	if got := session.FoldSurface(s.sess().Events()); len(got) != 3 {
		t.Fatalf("the log folds to %d messages", len(got))
	}
	s.mu.Lock()
	running := s.running
	s.mu.Unlock()
	if running {
		t.Fatal("the rewind left the session marked running")
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if w := postJSON(t, h, "/api/session/rewind", `{"entry":"`+ids[0]+`"}`); w.Code != http.StatusConflict {
		t.Fatalf("rewind while running = %d", w.Code)
	}
}

func TestReplayCarriesMessageModel(t *testing.T) {
	s, ids, cur := chatEnv(t)
	_ = ids
	s.resetView()
	s.replayHistory(session.FoldSurface(cur.Events()), modelsOf(cur.Events()))
	s.mu.Lock()
	defer s.mu.Unlock()
	var got []string
	for _, e := range s.entries {
		if e.Kind == "assistant" {
			got = append(got, e.Model)
		}
	}
	if len(got) != 2 || got[0] != "model-answer one" || got[1] != "model-answer two" {
		t.Fatalf("models = %q", got)
	}
}

func TestLiveAssistantEntryNamesItsModel(t *testing.T) {
	s, _, _, _, _, _ := sessionEnv(t)
	id := s.add(entry{Kind: "assistant", Text: "hi"})
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id && e.Model != "m" {
			t.Fatalf("model = %q, want m", e.Model)
		}
	}
}

func TestWorkspaceCtxMeter(t *testing.T) {
	s, _, _, _, _, _ := sessionEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/api/workspace", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	var ws struct {
		Ctx *ctxMeter `json:"ctx"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ws); err != nil || ws.Ctx == nil {
		t.Fatalf("workspace = %s (%v)", w.Body, err)
	}
	if ws.Ctx.Model != "m" || ws.Ctx.Total != ws.Ctx.Prompt+ws.Ctx.Completion {
		t.Fatalf("ctx = %+v", ws.Ctx)
	}
}
