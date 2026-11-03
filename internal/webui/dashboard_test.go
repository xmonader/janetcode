package webui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func dashPost(t *testing.T, s *server, op string, body any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/dashboard/"+op, strings.NewReader(string(b))))
	return w.Code, w.Body.String()
}

func dashGroups(t *testing.T, s *server) map[string][]dashRow {
	t.Helper()
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/dashboard", nil))
	var out struct {
		Groups   map[string][]dashRow `json:"groups"`
		Dispatch bool                 `json:"dispatch"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, w.Body.String())
	}
	return out.Groups
}

func groupOf(g map[string][]dashRow, id string) (string, dashRow) {
	for name, rows := range g {
		for _, r := range rows {
			if r.ID == id {
				return name, r
			}
		}
	}
	return "", dashRow{}
}

func TestDashboardGroupsAndPeek(t *testing.T) {
	s, _, _, cur, a, b := sessionEnv(t)
	reply := llm.TextMessage(llm.RoleAssistant, "all done here\nsecond line")
	if _, err := a.Append(session.Event{Type: session.TypeAssistant, Surface: true, Turn: 1, Message: &reply}); err != nil {
		t.Fatal(err)
	}
	// b: a background run waiting on a question; a: stays at rest.
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: time.Now(),
		Question: &session.BgQuestion{ID: "q1", Items: []session.BgQuestionItem{{Prompt: "Which db?"}}}})
	g := dashGroups(t, s)
	if grp, r := groupOf(g, cur.Meta.ID); grp != groupIdle || r.Kind != "live" || !r.Current {
		t.Errorf("live: %s %+v", grp, r)
	}
	if grp, r := groupOf(g, a.Meta.ID); grp != groupIdle || r.Kind != "stored" || r.Last != "all done here" || r.Replies {
		t.Errorf("stored: %s %+v", grp, r)
	}
	if grp, r := groupOf(g, b.Meta.ID); grp != groupAwaiting || r.Kind != "bg" || r.Waiting != "Which db?" || !r.Replies || !r.Stops {
		t.Errorf("bg question: %s %+v", grp, r)
	}
	// A running background run without a question is working.
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: time.Now()})
	if grp, r := groupOf(dashGroups(t, s), b.Meta.ID); grp != groupWorking || r.Replies || !r.Stops {
		t.Errorf("bg running: %s %+v", grp, r)
	}
	// A finished one is idle again.
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgDone, Started: time.Now(), Ended: time.Now()})
	if grp, _ := groupOf(dashGroups(t, s), b.Meta.ID); grp != groupIdle {
		t.Errorf("bg done: %s", grp)
	}
}

func TestDashboardRowsLiveStates(t *testing.T) {
	s, dir, _, cur, _, _ := sessionEnv(t)
	infos, _ := session.List(dir, s.cwd())
	for _, c := range []struct {
		name    string
		running bool
		waiting string
		group   string
	}{
		{"idle", false, "", groupIdle},
		{"working", true, "", groupWorking},
		{"awaiting", true, "Which db?", groupAwaiting},
	} {
		grp, r := groupOf(groupsOf(s.dashboardRows(dir, infos, cur.Meta.ID, c.running, c.waiting)), cur.Meta.ID)
		if grp != c.group || (c.waiting != "" && r.Waiting != c.waiting) {
			t.Errorf("%s: %s %+v", c.name, grp, r)
		}
	}
}

func groupsOf(rows []dashRow) map[string][]dashRow {
	g := map[string][]dashRow{}
	for _, r := range rows {
		g[r.Group] = append(g[r.Group], r)
	}
	return g
}

func TestDashboardReplyStopDispatch(t *testing.T) {
	s, _, _, cur, a, b := sessionEnv(t)
	long := exec.Command("sleep", "30")
	if err := long.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = long.Process.Kill(); _ = long.Wait() }()
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgRunning, PID: long.Process.Pid, Started: time.Now(),
		Question: &session.BgQuestion{ID: "q1", Items: []session.BgQuestionItem{{Prompt: "Which?"}, {Prompt: "And?"}}}})

	cases := []struct {
		name string
		op   string
		body dashBody
		code int
		want string
	}{
		{"empty reply", "reply", dashBody{ID: b.Meta.ID}, 400, "reply is required"},
		{"stored session", "reply", dashBody{ID: a.Meta.ID, Text: "hi"}, 409, "not running"},
		{"unknown", "reply", dashBody{ID: "nope", Text: "hi"}, 404, "unknown session"},
		{"stop stored", "stop", dashBody{ID: a.Meta.ID}, 409, "not running"},
		{"stop idle live", "stop", dashBody{ID: cur.Meta.ID}, 409, "nothing is running"},
		{"dispatch empty", "dispatch", dashBody{}, 400, "prompt is required"},
		{"dispatch unavailable", "dispatch", dashBody{Prompt: "x"}, 501, "not available"},
		{"bad op", "frob", dashBody{}, 404, ""},
	}
	for _, c := range cases {
		code, body := dashPost(t, s, c.op, c.body)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %q", c.name, code, body)
		}
	}
	// A reply to the bg question lands in the reply file, once per item.
	if code, body := dashPost(t, s, "reply", dashBody{ID: b.Meta.ID, Text: "postgres"}); code != 200 {
		t.Fatalf("reply: %d %s", code, body)
	}
	if ans, ok := session.TakeBgReply(b.Path(), "q1"); !ok || len(ans) != 2 || ans[0] != "postgres" {
		t.Fatalf("reply file: %v %v", ans, ok)
	}
	// Stop ends the process.
	if code, body := dashPost(t, s, "stop", dashBody{ID: b.Meta.ID}); code != 200 {
		t.Fatalf("stop: %d %s", code, body)
	}
	done := make(chan error, 1)
	go func() { done <- long.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("process still running after stop")
	}
	// Dispatch goes through the hook.
	var got string
	s.opts.Dispatch = func(_ context.Context, p string) (string, error) { got = p; return "new-id", nil }
	code, body := dashPost(t, s, "dispatch", dashBody{Prompt: "  fix tests "})
	if code != 200 || got != "fix tests" || !strings.Contains(body, `"id":"new-id"`) {
		t.Fatalf("dispatch: %d %s %q", code, body, got)
	}
}

func TestDashboardReplyToWaitingPermission(t *testing.T) {
	s, _, _, _, _, b := sessionEnv(t)
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgWaiting, PID: os.Getpid(), Started: time.Now(),
		Ask: &session.BgAsk{ID: "p1", Tool: "bash", Summary: "rm x", Asked: time.Now()}})
	if code, body := dashPost(t, s, "reply", dashBody{ID: b.Meta.ID, Text: "maybe"}); code != 400 || !strings.Contains(body, "allow, session or deny") {
		t.Fatalf("%d %s", code, body)
	}
	if code, body := dashPost(t, s, "reply", dashBody{ID: b.Meta.ID, Text: "Allow"}); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if d, ok := session.TakeBgAnswer(b.Path(), "p1"); !ok || d != "allow" {
		t.Fatalf("answer = %q %v", d, ok)
	}
}

// A run holding a pending question sorts into "awaiting" with it, finished
// or not; a finished one cannot be replied to.
func TestDashboardPendingQuestionNeedsYou(t *testing.T) {
	s, _, _, _, a, b := sessionEnv(t)
	now := time.Now()
	_ = session.WriteBgStatus(a.Path(), session.BgStatus{State: session.BgDone, Started: now, Ended: now,
		Pending: &session.BgPending{Text: "Drop the table?", About: "question", Asked: now}})
	_ = session.WriteBgStatus(b.Path(), session.BgStatus{State: session.BgRunning, PID: os.Getpid(), Started: now,
		Pending: &session.BgPending{Text: "May it run bash go test?", Choices: session.BgDecisions, About: "bash", Asked: now}})
	g := dashGroups(t, s)
	name, row := groupOf(g, a.Meta.ID)
	if name != groupAwaiting || row.Pending == nil || !row.Pending.Done || row.Replies {
		t.Fatalf("finished: %s %+v", name, row)
	}
	name, row = groupOf(g, b.Meta.ID)
	if name != groupAwaiting || row.Pending == nil || row.Pending.Done || len(row.Pending.Choices) != 3 || !row.Stops {
		t.Fatalf("running: %s %+v", name, row)
	}
}
