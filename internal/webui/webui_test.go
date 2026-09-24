package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/present"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// testHandler is the HTTP API and the embedded UI for one server.
func testHandler(opts Options) http.Handler { return newServer(context.Background(), opts).handler() }

func TestListenAddr(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", "127.0.0.1:8787"},
		{"8787", "127.0.0.1:8787"},
		{":9090", "127.0.0.1:9090"},
		{"127.0.0.1:80", "127.0.0.1:80"},
		{"0.0.0.0:8787", "0.0.0.0:8787"},
	} {
		if got := ListenAddr(c.in); got != c.want {
			t.Errorf("ListenAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !Loopback("127.0.0.1:8787") || Loopback("0.0.0.0:8787") {
		t.Fatal("loopback detection")
	}
}

type scripted struct {
	mu        sync.Mutex
	responses []*llm.Response
}

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	s.mu.Lock()
	r := s.responses[0]
	s.responses = s.responses[1:]
	s.mu.Unlock()
	for _, b := range r.Message.Blocks {
		switch b.Type {
		case llm.BlockText:
			emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
		case llm.BlockToolUse:
			bb := b
			emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
			emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
		}
	}
	return r, nil
}

func testServer(t *testing.T, responses ...*llm.Response) *httptest.Server {
	t.Helper()
	ts, _ := testApp(t, responses...)
	return ts
}

func testApp(t *testing.T, responses ...*llm.Response) (*httptest.Server, *agent.Agent) {
	t.Helper()
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{
		Provider: &scripted{responses: responses},
		Tools:    tool.NewRegistry(tool.WriteFile{Root: root}),
		Model:    "m",
		Log:      sess,
		Root:     root,
	}
	h := testHandler(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test"})
	return httptest.NewServer(h), ag
}

func TestWorkspaceIsStructured(t *testing.T) {
	ts := testServer(t)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var ws workspace
	if err := json.NewDecoder(res.Body).Decode(&ws); err != nil {
		t.Fatal(err)
	}
	if ws.Todos == nil {
		ws.Todos = []todoItem{}
	}
	page, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(page.Body)
	page.Body.Close()
	html := string(body)
	for _, want := range []string{"New session", "Plan", "Changed files", "Background jobs", "Subagents", "Scripts", "Trajectory", "id=\"sessions\"", "id=\"composer\"", "id=\"mode-chip\"", "id=\"mode-menu\"", "id=\"questions\"", "id=\"mode-auto\"", "id=\"trajectory\"", "work-tabs", "new-session"} {
		if !strings.Contains(html, want) {
			t.Errorf("page missing %q", want)
		}
	}
	nr, err := http.Post(ts.URL+"/api/new", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	nr.Body.Close()
	if nr.StatusCode != 200 {
		t.Fatalf("new chat %d", nr.StatusCode)
	}
}

func TestIndexServesThePage(t *testing.T) {
	ts := testServer(t)
	defer ts.Close()
	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !bytes.Contains(body, []byte("janetcode")) {
		t.Fatalf("status %d body %s", res.StatusCode, body[:min(200, len(body))])
	}
	// The page must carry the server's build stamped in (the stale-UI
	// toast compares it against the hello event's version).
	if !bytes.Contains(body, []byte(`window.__janetUIBuild = "test";`)) {
		t.Fatalf("index.html missing stamped build: %s", body[:min(200, len(body))])
	}
	if bytes.Contains(body, []byte("__JANET_UI_BUILD__")) {
		t.Fatal("index.html served with an unstamped build placeholder")
	}
	css, err := http.Get(ts.URL + "/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css.Body.Close()
	if css.StatusCode != 200 {
		t.Fatalf("css %d", css.StatusCode)
	}
}

func TestPromptStreamsOnSSE(t *testing.T) {
	ts := testServer(t, &llm.Response{
		Message:    llm.TextMessage(llm.RoleAssistant, "hello from the web"),
		StopReason: llm.StopEndTurn,
	})
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	got := make(chan string, 8)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var env envelope
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env) != nil {
				continue
			}
			if env.Type == "entry" && env.Entry != nil && strings.Contains(env.Entry.Text, "hello from the web") {
				got <- env.Entry.Text
				return
			}
		}
	}()

	body, _ := json.Marshal(map[string]string{"text": "hi there"})
	pr, err := http.Post(ts.URL+"/api/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != 200 {
		t.Fatalf("prompt %d", pr.StatusCode)
	}
	select {
	case text := <-got:
		if !strings.Contains(text, "hello from the web") {
			t.Fatalf("got %q", text)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for the streamed answer")
	}
	// The answer streams before the turn has finished writing its
	// session; returning here raced the TempDir cleanup ("directory not
	// empty", about one run in twelve).
	waitTurnDone(t, ts)
}

// waitTurnDone polls /api/state until no turn is running.
func waitTurnDone(t *testing.T, ts *httptest.Server) {
	t.Helper()
	waitFor(t, func() bool {
		res, err := http.Get(ts.URL + "/api/state")
		if err != nil {
			return false
		}
		defer res.Body.Close()
		var st struct {
			Status struct {
				Running bool `json:"running"`
			} `json:"status"`
		}
		return json.NewDecoder(res.Body).Decode(&st) == nil && !st.Status.Running
	})
}

func TestPermissionRoundTrip(t *testing.T) {
	ts := testServer(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	asked := make(chan permPayload, 1)
	done := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var env envelope
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &env) != nil {
				continue
			}
			if env.Type == "permission" && env.Perm != nil && env.Perm.Name == "write_file" {
				select {
				case asked <- *env.Perm:
				default:
				}
			}
			if env.Type == "entry" && env.Entry != nil && env.Entry.Kind == "assistant" && strings.Contains(env.Entry.Text, "written") {
				done <- env.Entry.Text
				return
			}
		}
	}()

	body, _ := json.Marshal(map[string]string{"text": "make n.txt"})
	pr, err := http.Post(ts.URL+"/api/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()

	select {
	case p := <-asked:
		if p.Name != "write_file" {
			t.Fatalf("perm %+v", p)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no permission prompt")
	}
	dec, _ := json.Marshal(map[string]string{"decision": "allow"})
	ar, err := http.Post(ts.URL+"/api/permission", "application/json", bytes.NewReader(dec))
	if err != nil {
		t.Fatal(err)
	}
	ar.Body.Close()
	if ar.StatusCode != 200 {
		t.Fatalf("allow %d", ar.StatusCode)
	}
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("turn did not finish after allow")
	}
	st, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Body.Close()
	var snap struct {
		Entries []entry `json:"entries"`
	}
	if err := json.NewDecoder(st.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	var sawWrite bool
	for _, e := range snap.Entries {
		if e.Kind == "tool" && e.Name == "write_file" && e.Done {
			sawWrite = true
		}
	}
	if !sawWrite {
		t.Fatalf("state missing the write: %+v", snap.Entries)
	}
}

func TestWorkspaceExposesSessionChrome(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	ag.AutoMode = true
	ag.WriteTodos([]agent.Todo{{Text: "ship it", Status: agent.TodoDoing}})
	res, err := http.Get(ts.URL + "/api/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var ws workspace
	if err := json.NewDecoder(res.Body).Decode(&ws); err != nil {
		t.Fatal(err)
	}
	if ws.Mode != "AUTO" {
		t.Fatalf("mode = %q", ws.Mode)
	}
	if len(ws.Todos) != 1 || ws.Todos[0].Text != "ship it" || ws.Todos[0].Status != agent.TodoDoing {
		t.Fatalf("todos = %+v", ws.Todos)
	}
	if ws.Jobs == nil {
		ws.Jobs = []jobItem{}
	}
	if ws.Agents == nil {
		ws.Agents = []agentItem{}
	}
	hello, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer hello.Body.Close()
	raw, err := io.ReadAll(hello.Body)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Hello struct {
			Mode string `json:"mode"`
		} `json:"hello"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Hello.Mode != "AUTO" {
		t.Fatalf("hello.mode = %q", snap.Hello.Mode)
	}
	if !bytes.Contains(raw, []byte(`"questions"`)) {
		t.Fatalf("state missing questions key: %s", raw)
	}
}

func TestAskQuestionRoundTrip(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	if ag.Asker == nil {
		t.Fatal("web handler must install an Asker")
	}
	got := make(chan []agent.Answer, 1)
	errc := make(chan error, 1)
	go func() {
		ans, err := ag.Asker.Ask(context.Background(), []agent.Question{{
			Prompt:  "Which approach?",
			Options: []agent.AskOption{{Label: "A", Description: "first"}, {Label: "B"}},
		}})
		if err != nil {
			errc <- err
			return
		}
		got <- ans
	}()
	var qs []agent.Question
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		var snap struct {
			Questions []agent.Question `json:"questions"`
		}
		if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
			res.Body.Close()
			t.Fatal(err)
		}
		res.Body.Close()
		if len(snap.Questions) > 0 {
			qs = snap.Questions
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(qs) != 1 || qs[0].Prompt != "Which approach?" {
		t.Fatalf("questions on /api/state = %+v", qs)
	}
	body, _ := json.Marshal(map[string]any{"answers": []agent.Answer{{Label: "A"}}})
	pr, err := http.Post(ts.URL+"/api/question", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != 200 {
		t.Fatalf("question %d", pr.StatusCode)
	}
	select {
	case ans := <-got:
		if len(ans) != 1 || ans[0].Label != "A" {
			t.Fatalf("answers = %+v", ans)
		}
	case err := <-errc:
		t.Fatal(err)
	case <-time.After(3 * time.Second):
		t.Fatal("Ask did not return after /api/question")
	}
}

func TestTrajectoryLedgerFromSessionEvents(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	if _, err := ag.Log.Append(session.Event{
		Type: session.TypeUserMessage, Turn: 1, Surface: true,
		Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look at main.go"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Log.Append(session.Event{
		Type: session.TypeToolCall, Turn: 1, Step: 1, Name: "read_file", CallID: "c1",
		Arguments: `{"path":"main.go"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.Log.Append(session.Event{
		Type: session.TypeToolResult, Turn: 1, Step: 1, Name: "read_file", CallID: "c1",
		Output: "package main", DurationMs: 12,
	}); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(ts.URL + "/api/trajectory")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Records []trajRecord `json:"records"`
		Turns   []trajTurn   `json:"turns"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// The ledger folds the log: the call and its result are one record.
	var user, tool *trajRecord
	for i := range body.Records {
		switch body.Records[i].Kind {
		case "user":
			user = &body.Records[i]
		case "tool":
			tool = &body.Records[i]
		}
	}
	if user == nil || !strings.Contains(user.Preview, "main.go") {
		t.Fatalf("ledger missing the user record: %+v", body.Records)
	}
	if tool == nil || tool.Tool != "read_file" || tool.Duration != 12 || tool.Result == "" {
		t.Fatalf("the call never met its result: %+v", tool)
	}
	if len(body.Turns) != 1 || body.Turns[0].Tools != 1 || !strings.Contains(body.Turns[0].Heading, "turn 1") {
		t.Fatalf("turns = %+v", body.Turns)
	}
	// The detail is keyed by the record's index and carries the body.
	detail, err := http.Get(ts.URL + "/api/trajectory?n=" + strconv.Itoa(tool.Index))
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	if detail.StatusCode != 200 {
		t.Fatalf("detail %d", detail.StatusCode)
	}
	var one struct {
		Record trajRecord `json:"record"`
	}
	if err := json.NewDecoder(detail.Body).Decode(&one); err != nil {
		t.Fatal(err)
	}
	if one.Record.Input == "" || one.Record.Output != "package main" {
		t.Fatalf("detail = %+v", one.Record)
	}
	if missing, err := http.Get(ts.URL + "/api/trajectory?n=999"); err != nil {
		t.Fatal(err)
	} else {
		missing.Body.Close()
		if missing.StatusCode != http.StatusNotFound {
			t.Fatalf("a missing record should 404, got %d", missing.StatusCode)
		}
	}
}

// The timeline endpoint projects the same fold the ledger shows: spans
// in the mode's domain, lanes fixed to input/model/tools, a boundary
// per turn, and the waiting share on model spans.
func TestTrajectoryTimelineProjection(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	appendEvent := func(e session.Event) {
		t.Helper()
		if _, err := ag.Log.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(session.Event{Type: session.TypeTurnStart, Turn: 1})
	appendEvent(session.Event{Type: session.TypeUserMessage, Turn: 1,
		Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "hi"}}}})
	appendEvent(session.Event{Type: session.TypeStepStart, Turn: 1, Step: 1})
	appendEvent(session.Event{Type: session.TypeAssistant, Turn: 1, Step: 1,
		Message: &llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: "hello"}}},
		TTFTMs:  300, DecodeMs: 700,
		Usage: &llm.Usage{InputTokens: 10, OutputTokens: 5}})

	res, err := http.Get(ts.URL + "/api/trajectory/timeline?mode=sequence")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var tl struct {
		Mode  string         `json:"mode"`
		Spans []trajSpan     `json:"spans"`
		Turns []trajBoundary `json:"turns"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tl); err != nil {
		t.Fatal(err)
	}
	if tl.Mode != "sequence" || len(tl.Spans) != 2 {
		t.Fatalf("sequence = %+v", tl)
	}
	if len(tl.Turns) != 1 || tl.Turns[0].Turn != 1 || tl.Turns[0].Time != 0 {
		t.Fatalf("boundaries = %+v", tl.Turns)
	}
	var model *trajSpan
	for i := range tl.Spans {
		if tl.Spans[i].Kind == "message" {
			model = &tl.Spans[i]
		}
		if tl.Spans[i].Lane < 0 || tl.Spans[i].Lane > 2 {
			t.Fatalf("lane out of range: %+v", tl.Spans[i])
		}
	}
	if model == nil || model.Lane != 1 || model.TTFT != 0.3 {
		t.Fatalf("the model span lost its lane or its waiting share: %+v", tl.Spans)
	}
	if bad, err := http.Get(ts.URL + "/api/trajectory/timeline?mode=sideways"); err != nil {
		t.Fatal(err)
	} else {
		bad.Body.Close()
		if bad.StatusCode != http.StatusBadRequest {
			t.Fatalf("a bad mode should 400, got %d", bad.StatusCode)
		}
	}
}

// The raw endpoint keeps the per-event view for when the ledger itself
// is what looks wrong.
func TestTrajectoryRawKeepsTheEventLog(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	if _, err := ag.Log.Append(session.Event{Type: session.TypeUserMessage, Turn: 1,
		Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "hi"}}}}); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(ts.URL + "/api/trajectory/raw")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Events []trajRow `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Kind != "user" {
		t.Fatalf("raw events = %+v", body.Events)
	}
}

func TestWebStatusDressesThinking(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.running = true
	s.phase = "thinking"
	s.started = time.Unix(1_700_000_000, 0)
	st := s.statusNow()
	if !flavor.Known(st.Phase) {
		t.Fatalf("thinking phase = %q", st.Phase)
	}
	s.phase = "running bash"
	if got := s.statusNow().Phase; got != "running bash" {
		t.Fatalf("tool phase was dressed: %q", got)
	}
}

type exploreProv struct{}

func (exploreProv) Name() string { return "fake" }
func (exploreProv) Stream(_ context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	_ = emit
	last := req.Messages[len(req.Messages)-1]
	if last.Role == llm.RoleUser && len(last.ToolUses()) == 0 && last.Blocks[0].Type != llm.BlockToolResult {
		r := &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
		}}, StopReason: llm.StopToolUse}
		return r, nil
	}
	text := ""
	if len(last.Blocks) > 0 {
		text = last.Blocks[0].Text
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "REPORT: a.txt says "+strings.TrimSpace(text)), StopReason: llm.StopEndTurn}, nil
}

func TestAgentTranscriptAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Provider: exploreProv{}, Model: "m", System: "sys", AutoApprove: true, Log: sess}
	task := &agent.Task{Parent: ag, Root: root}
	ag.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)
	ts := httptest.NewServer(testHandler(Options{Agent: ag, Session: sess, Cwd: root, Version: "test"}))
	defer ts.Close()
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"explore one"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "REPORT: a.txt says") {
		t.Fatalf("child report = %q", out)
	}
	ids := task.ChildIDs()
	if len(ids) != 1 {
		t.Fatalf("children = %v", ids)
	}
	res, err := http.Get(ts.URL + "/api/agent?id=" + ids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	raw, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(raw), ids[0]) || !strings.Contains(string(raw), "a.txt") {
		t.Fatalf("agent api missing child transcript: %s", raw)
	}
	ws, err := http.Get(ts.URL + "/api/workspace")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Body.Close()
	var snap workspace
	if err := json.NewDecoder(ws.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Agents) != 1 || snap.Agents[0].ID != ids[0] {
		t.Fatalf("workspace agents = %+v", snap.Agents)
	}
}

func TestJobLogAPI(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	jobs := &tool.Jobs{Dir: t.TempDir()}
	ag := &agent.Agent{Model: "m", Log: sess, Tools: tool.NewRegistry()}
	ts := httptest.NewServer(testHandler(Options{Agent: ag, Session: sess, Jobs: jobs, Cwd: root, Version: "test"}))
	defer ts.Close()
	job, err := jobs.Start(root, "echo hello-from-job")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := jobs.Wait(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	res, err := http.Get(ts.URL + "/api/job?id=" + strconv.Itoa(job.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(raw), "hello-from-job") {
		t.Fatalf("job api = %d %s", res.StatusCode, raw)
	}
}

func TestPTCRunAPI(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello-from-ptc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Model: "m", Log: sess, AutoApprove: true}
	rc := &agent.RunCode{Parent: ag}
	ag.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, rc)
	ts := httptest.NewServer(testHandler(Options{Agent: ag, Session: sess, Cwd: root, Version: "test"}))
	defer ts.Close()
	out, err := rc.Run(context.Background(), json.RawMessage(`{"description":"read it","code":[{"name":"read_file","input":{"path":"a.txt"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello-from-ptc") {
		t.Fatalf("run_code = %q", out)
	}
	list, err := http.Get(ts.URL + "/api/ptc")
	if err != nil {
		t.Fatal(err)
	}
	defer list.Body.Close()
	raw, _ := io.ReadAll(list.Body)
	if !strings.Contains(string(raw), "run_code") {
		t.Fatalf("ptc list = %s", raw)
	}
	detail, err := http.Get(ts.URL + "/api/ptc?n=1")
	if err != nil {
		t.Fatal(err)
	}
	defer detail.Body.Close()
	body, _ := io.ReadAll(detail.Body)
	if detail.StatusCode != 200 || !strings.Contains(string(body), "read_file") || !strings.Contains(string(body), "hello-from-ptc") {
		t.Fatalf("ptc detail = %d %s", detail.StatusCode, body)
	}
}

func TestColorTokensUnchanged(t *testing.T) {
	css, err := staticFS.ReadFile("static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"--blue: #3b5bdb;", "--bg: #ffffff;", "--sidebar: #f6f6f8;", "--blue: #4563ee;", "--bg: #0d0d10;"} {
		if !bytes.Contains(css, []byte(tok)) {
			t.Errorf("stylesheet lost %q", tok)
		}
	}
}

// TestPermissionLadderPayloadAndChoice: the card is given the whole
// ladder of rules and which one to start on, and "always" saves the rung
// the browser sends back rather than the one the server proposed.
func TestPermissionLadderPayloadAndChoice(t *testing.T) {
	ts, ag := testApp(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"docs/n.txt","content":"x\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	defer ts.Close()
	// SaveRule runs on the turn's goroutine while the test reads what it
	// saved, so both go through this mutex.
	var mu sync.Mutex
	var saved []string
	ag.SaveRule = func(r perm.Rule) error {
		mu.Lock()
		defer mu.Unlock()
		saved = append(saved, r.String())
		return nil
	}
	savedRules := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), saved...)
	}

	body, _ := json.Marshal(map[string]string{"text": "make it"})
	pr, err := http.Post(ts.URL+"/api/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()

	var p permPayload
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		var snap struct {
			Permission *permPayload `json:"permission"`
		}
		err = json.NewDecoder(res.Body).Decode(&snap)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if snap.Permission != nil && snap.Permission.Name == "write_file" {
			p = *snap.Permission
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if p.Name != "write_file" {
		t.Fatal("no permission prompt")
	}
	if len(p.Rungs) != 2 || p.Rungs[0].Rule != "write_file(docs/n.txt)" || p.Rungs[1].Rule != "write_file(docs/*)" || p.RuleIndex != 1 {
		t.Fatalf("rungs = %+v index = %d", p.Rungs, p.RuleIndex)
	}
	if p.Rule != p.Rungs[p.RuleIndex].Rule {
		t.Fatalf("rule %q is not the rung it starts on: %+v", p.Rule, p.Rungs)
	}
	// The narrowest rung, which is not the one the server proposed.
	dec, _ := json.Marshal(map[string]string{"decision": "always", "rule": p.Rungs[0].Rule})
	ar, err := http.Post(ts.URL+"/api/permission", "application/json", bytes.NewReader(dec))
	if err != nil {
		t.Fatal(err)
	}
	ar.Body.Close()
	if ar.StatusCode != 200 {
		t.Fatalf("always %d", ar.StatusCode)
	}
	for time.Now().Before(deadline) && len(savedRules()) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if got := savedRules(); len(got) != 1 || got[0] != "write_file(docs/n.txt)" {
		t.Fatalf("saved = %v, want the rung the browser chose", got)
	}
	if v, r := ag.RulesSnapshot().Decide("write_file", "docs/n.txt"); v != perm.Allowed || r.String() != "write_file(docs/n.txt)" {
		t.Fatalf("rules = %v %s", v, r)
	}
	// The turn writes the file after the answer, so the test waits for it
	// to finish: otherwise the cleanup of the temporary root races the
	// write.
	for done := false; !done && time.Now().Before(deadline); {
		res, err := http.Get(ts.URL + "/api/state")
		if err != nil {
			t.Fatal(err)
		}
		var snap struct {
			Entries []entry `json:"entries"`
		}
		err = json.NewDecoder(res.Body).Decode(&snap)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range snap.Entries {
			if e.Kind == "assistant" && strings.Contains(e.Text, "written") {
				done = true
			}
		}
		if !done {
			time.Sleep(20 * time.Millisecond)
		}
	}
}

// TestWebSessionListCarriesForkLineage: the web UI's session list gets
// the same shape the terminal picker draws — a fork under the session it
// was copied from, and a fork whose parent is not listed saying so.
func TestWebSessionListCarriesForkLineage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	root := t.TempDir()
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	write := func(s *session.Session, text string) {
		msg := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Text: text}}}
		if _, err := s.Append(session.Event{Type: session.TypeUserMessage, Message: &msg}); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := session.New(dir, root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	write(parent, "the original")
	child, err := session.Fork(dir, parent, "the fork")
	if err != nil {
		t.Fatal(err)
	}
	stray, err := session.New(dir, root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	write(stray, "a stray fork")
	stray.Meta.ForkOf = "20000101-000000-dead"
	if err := stray.SetTitle("a stray fork"); err != nil {
		t.Fatal(err)
	}

	ag := &agent.Agent{Provider: &scripted{}, Model: "m", Log: parent}
	ts := httptest.NewServer(testHandler(Options{Agent: ag, Session: parent, Cwd: root, Version: "test"}))
	defer ts.Close()
	res, err := http.Get(ts.URL + "/api/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rows []struct {
		ID      string `json:"id"`
		Depth   int    `json:"depth"`
		Prefix  string `json:"prefix"`
		Note    string `json:"note"`
		Current bool   `json:"current"`
	}
	if err := json.NewDecoder(res.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, r := range rows {
		byID[r.ID] = i
	}
	pi, ci, si := byID[parent.Meta.ID], byID[child.Meta.ID], byID[stray.Meta.ID]
	if rows[pi].Depth != 0 || !rows[pi].Current {
		t.Fatalf("parent row = %+v", rows[pi])
	}
	if rows[ci].Depth != 1 || ci != pi+1 || rows[ci].Prefix == "" {
		t.Fatalf("fork row = %+v (parent at %d, fork at %d)", rows[ci], pi, ci)
	}
	if rows[si].Depth != 0 || !strings.Contains(rows[si].Note, "not listed here") {
		t.Fatalf("stray row = %+v", rows[si])
	}
}

// --web on a port that is already taken should not refuse to start: it
// serves on a random port and reports the real URL, which is what the
// user then opens.
func TestRunFallsBackToARandomPortWhenTheAddressIsBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	_, port, err := net.SplitHostPort(busy.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan string, 1)
	go func() {
		if err := Run(ctx, Options{Addr: "127.0.0.1:" + port, Opened: func(u string) { opened <- u }}); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()

	var url string
	select {
	case url = <-opened:
	case <-time.After(3 * time.Second):
		t.Fatal("Run never reported its URL")
	}
	if strings.HasSuffix(url, ":"+port) {
		t.Fatalf("fell back onto the busy port: %s", url)
	}
	res, err := http.Get(url + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("the fallback port does not serve: %d", res.StatusCode)
	}
}

// A free explicit port is still used as asked — the fallback only
// answers a busy one.
func TestRunUsesAFreeExplicitPortAsAsked(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan string, 1)
	go func() {
		if err := Run(ctx, Options{Addr: addr, Opened: func(u string) { opened <- u }}); err != nil {
			t.Errorf("Run: %v", err)
		}
	}()
	select {
	case url := <-opened:
		if !strings.HasSuffix(url, addr[strings.LastIndex(addr, ":"):]) {
			t.Fatalf("served %s, asked for %s", url, addr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run never reported its URL")
	}
}

// /perf and /usage answer as structured cards in the thread — the same
// numbers the text sheets print, but a payload the front-end can draw.
func TestPerfAndUsageAnswerAsCards(t *testing.T) {
	ts, _ := testApp(t)
	defer ts.Close()
	post := func(text string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		res, err := http.Post(ts.URL+"/api/command", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", text, res.StatusCode)
		}
	}
	post("/perf")
	post("/usage")
	res, err := http.Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st struct {
		Entries []entry `json:"entries"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	var perf, usage *entry
	for i := range st.Entries {
		switch st.Entries[i].Kind {
		case "perf":
			perf = &st.Entries[i]
		case "usage":
			usage = &st.Entries[i]
		}
	}
	if perf == nil || usage == nil {
		t.Fatalf("entries = %+v", st.Entries)
	}
	data, err := json.Marshal(perf.Data)
	if err != nil {
		t.Fatal(err)
	}
	var pc perfCard
	if err := json.Unmarshal(data, &pc); err != nil {
		t.Fatal(err)
	}
	if len(pc.Slices) != 4 {
		t.Fatalf("perf slices = %+v", pc.Slices)
	}
	names := map[string]bool{}
	for _, sl := range pc.Slices {
		names[sl.Name] = true
	}
	for _, want := range []string{"model", "tools", "you", "harness"} {
		if !names[want] {
			t.Fatalf("perf slices miss %q: %+v", want, pc.Slices)
		}
	}
	udata, err := json.Marshal(usage.Data)
	if err != nil {
		t.Fatal(err)
	}
	var uc usageCard
	if err := json.Unmarshal(udata, &uc); err != nil {
		t.Fatal(err)
	}
	if uc.Models == nil {
		t.Fatalf("usage models should be an empty list, not nil: %+v", uc)
	}
}

// The perf card's clock arithmetic, with the clock held still: the open
// time is measured against openedAt, and the idle remainder cannot go
// negative.
func TestPerfCardMeasuresAgainstOpenedAt(t *testing.T) {
	ag := &agent.Agent{Tools: tool.NewRegistry()}
	s := newServer(context.Background(), Options{Agent: ag})
	s.openedAt = time.Now().Add(-3 * time.Minute)
	pc := s.perfCard()
	if pc.OpenMs < 180_000 {
		t.Fatalf("open = %dms, want ~3 minutes", pc.OpenMs)
	}
	if pc.IdleMs != pc.OpenMs-pc.TurnMs || pc.IdleMs < 0 {
		t.Fatalf("idle = %d, open = %d, turn = %d", pc.IdleMs, pc.OpenMs, pc.TurnMs)
	}
}

// A server started on a session with history starts with that history on
// the thread — the web UI used to open blank until something new happened.
func TestNewServerSeedsTheThreadFromSessionHistory(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	appendMsg := func(role llm.Role, text string) {
		t.Helper()
		m := llm.TextMessage(role, text)
		if _, err := sess.Append(session.Event{Type: map[llm.Role]string{llm.RoleUser: session.TypeUserMessage, llm.RoleAssistant: session.TypeAssistant}[role], Turn: 1, Message: &m, Surface: true}); err != nil {
			t.Fatal(err)
		}
	}
	appendMsg(llm.RoleUser, "build me a small library")
	appendMsg(llm.RoleAssistant, "here is the plan:\n\n```py\n# doubles\nreturn x * 2\n```")
	s := newServer(context.Background(), Options{Agent: &agent.Agent{Tools: tool.NewRegistry()}, Session: sess})
	_, ents, _, _, _ := s.snapshot()
	var sawUser, sawAssistant bool
	for _, e := range ents {
		if e.Kind == "user" && strings.Contains(e.Text, "small library") {
			sawUser = true
		}
		if e.Kind == "assistant" && strings.Contains(e.Text, "doubles") {
			sawAssistant = true
		}
	}
	if !sawUser || !sawAssistant {
		t.Fatalf("seeded entries = %+v", ents)
	}
}

// The header's model button reads /api/models: the providers and their
// models from the config merged with what each provider actually serves
// right now, the active provider first, the current model marked.
func TestModelsAPIListsWhatTheConfigKnows(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Provider: "zhipu",
		Model:    "glm-4",
		Providers: map[string]config.Provider{
			"zhipu":     {Models: []string{"glm-4", "glm-4-plus"}, Model: "glm-4"},
			"openai":    {Models: []string{"gpt-4o"}, BaseURL: "https://openrouter.ai/api/v1"},
			"anthropic": {},
		},
		Models: map[string]config.ModelMeta{
			"glm-4":       {ContextWindow: 128000},
			"glm-4-plus":  {ContextWindow: 128000},
			"qwen3-coder": {ContextWindow: 262144},
		},
	}
	ag := &agent.Agent{Tools: tool.NewRegistry(), Model: "glm-4"}
	var calls atomic.Int32
	srv := newServer(context.Background(), Options{
		Agent: ag, Session: sess, Provider: "zhipu", Config: cfg, Cwd: root,
		BuildProvider: func(name string) (llm.Provider, error) {
			if name == "anthropic" {
				return nil, errNoKeys
			}
			if name == "zhipu" {
				return liveProvider{lister: func() ([]llm.ModelInfo, error) {
					calls.Add(1)
					return []llm.ModelInfo{
						{ID: "glm-4", ContextWindow: 128000},
						{ID: "glm-4-plus", ContextWindow: 200000}, // fresher than the config's
					}, nil
				}}, nil
			}
			return liveProvider{lister: func() ([]llm.ModelInfo, error) {
				calls.Add(1)
				return []llm.ModelInfo{
					{ID: "gpt-4o", ContextWindow: 128000},
					{ID: "gpt-5", ContextWindow: 400000},
				}, nil
			}}, nil
		},
	})
	ts := httptest.NewServer(srv.handler())
	defer ts.Close()

	var body struct {
		Current string       `json:"current"`
		Groups  []modelGroup `json:"groups"`
	}
	get := func() {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/models")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
	}
	get()
	if body.Current != "glm-4" {
		t.Fatalf("current = %q", body.Current)
	}
	if len(body.Groups) == 0 || body.Groups[0].Provider != "zhipu" {
		t.Fatalf("the active provider should lead: %+v", body.Groups)
	}
	rows := map[string]modelRow{}
	for _, g := range body.Groups {
		for _, m := range g.Models {
			rows[m.Model] = m
		}
	}
	if !rows["glm-4"].Current {
		t.Fatalf("the current model wears the ✓: %+v", rows["glm-4"])
	}
	// An openai entry pointed at OpenRouter is headed by the service it
	// reaches, not "openai".
	for _, g := range body.Groups {
		if g.Provider == "openai" && g.Label != "openrouter" {
			t.Fatalf("openai group label = %q", g.Label)
		}
	}
	if rows["qwen3-coder"].Model != "qwen3-coder" {
		t.Fatalf("learned metas stay listed: %+v", rows["qwen3-coder"])
	}
	// Live answers join their provider's group, and a fresher context
	// window wins over the config's stale number.
	if !rows["gpt-5"].Live {
		t.Fatalf("gpt-5 came from the live sweep: %+v", rows["gpt-5"])
	}
	if rows["glm-4-plus"].Context != 200000 {
		t.Fatalf("the live context should upgrade the stale one: %+v", rows["glm-4-plus"])
	}
	// The sweep runs once; the next read is cache. Two providers answer,
	// so one sweep is two lister calls.
	get()
	if calls.Load() != 2 {
		t.Fatalf("the live sweep ran %d lister calls for two reads", calls.Load())
	}
	// A provider that answers nothing and lists nothing has no group.
	for _, g := range body.Groups {
		if g.Provider == "anthropic" {
			t.Fatalf("an empty provider should not get a group: %+v", g)
		}
	}
}

var errNoKeys = errors.New("no keys in the test")

// liveProvider is a provider that only knows how to list: every other
// method belongs to the embedded nil and would panic if called, which
// the switcher never does.
type liveProvider struct {
	llm.Provider
	lister func() ([]llm.ModelInfo, error)
}

func (p liveProvider) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	return p.lister()
}

// The answer a turn streams into wears the turn's bill when the turn
// finishes: tokens from the agent's usage report, wall time from begin().
func TestAssistantEntryIsStampedWithTheTurnsBill(t *testing.T) {
	s := newServer(context.Background(), Options{Agent: &agent.Agent{Tools: tool.NewRegistry()}})
	s.started = time.Now().Add(-1500 * time.Millisecond)
	s.apply(agent.Event{Kind: agent.EvText, Text: "hello there"})
	s.apply(agent.Event{Kind: agent.EvTurnDone, Usage: llm.Usage{
		InputTokens: 100, CacheReadTokens: 40, CacheWriteTokens: 10, OutputTokens: 25,
	}})
	var got *entry
	for i := range s.entries {
		if s.entries[i].Kind == "assistant" {
			got = s.entries[i]
		}
	}
	if got == nil {
		t.Fatal("no assistant entry")
	}
	if got.TokIn != 150 || got.TokOut != 25 {
		t.Fatalf("usage stamp = in %d out %d, want 150/25", got.TokIn, got.TokOut)
	}
	if got.Ms == 0 {
		t.Fatal("duration stamp missing")
	}
	if got.At.IsZero() {
		t.Fatal("timestamp missing")
	}
}

// The terminal endpoint bridges a websocket to a shell on a pty: input
// round-trips through a real process, and the shell runs in the session
// directory.
func TestTerminalBridgeRunsAShellInTheSessionDirectory(t *testing.T) {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(context.Background(), Options{Agent: &agent.Agent{Tools: tool.NewRegistry()}, Session: sess, Cwd: root})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()

	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(ts.URL, "http")+"/api/term", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")

	send := func(v map[string]any) {
		data, _ := json.Marshal(v)
		if err := ws.Write(context.Background(), websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
	}
	send(map[string]any{"type": "resize", "rows": 24, "cols": 80})
	// A marker only this test's directory can produce.
	send(map[string]any{"type": "in", "data": "pwd; echo TERM-DONE-$((6*7))\n"})

	deadline := time.After(10 * time.Second)
	buf := ""
	for !strings.Contains(buf, "TERM-DONE-42") {
		select {
		case <-deadline:
			t.Fatalf("no shell output; got %q", buf)
		default:
		}
		_, data, err := ws.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Type string `json:"type"`
			Data string `json:"data"`
		}
		if json.Unmarshal(data, &m) == nil && m.Type == "out" {
			buf += m.Data
		}
	}
	if !strings.Contains(buf, root) {
		t.Fatalf("the shell is not in the session directory; got %q", buf)
	}
}

// TestWebStatusNamesStreamingWrite: while a write_file's arguments
// stream, the status line names the file and how much has arrived —
// what the TUI shows — instead of a rotating filler word for a minute.
func TestWebStatusNamesStreamingWrite(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.running = true
	call := &agent.ToolCall{ID: "c1", Name: "write_file"}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	if got := s.statusNow().Phase; got != "preparing write_file" {
		t.Fatalf("start phase = %q", got)
	}
	call.Input = json.RawMessage(`{"path": "main.go", "content": "package main\n\nfunc main() {\n`)
	s.apply(agent.Event{Kind: agent.EvToolInput, Call: call})
	if got := s.statusNow().Phase; !strings.HasPrefix(got, "write_file main.go · writing 4 lines") {
		t.Fatalf("streaming phase = %q", got)
	}
	s.apply(agent.Event{Kind: agent.EvText, Text: "Done."})
	if got := s.statusNow().Phase; got != "writing" {
		t.Fatalf("text phase = %q", got)
	}
}

// TestPermissionCardShowsTheWholeCommand: a model that prefixes every
// command with "cd <session dir> &&" pushed the real command past the
// 80-column summary, so the card asked to approve an absolute path. The
// redundant cd is dropped, and a command the summary cannot hold is sent
// whole as the card's preview.
func TestPermissionCardShowsTheWholeCommand(t *testing.T) {
	cwd := t.TempDir()
	s := newServer(context.Background(), Options{Cwd: cwd})
	in, _ := json.Marshal(map[string]string{"command": "cd " + cwd + " && go mod init wordfreq"})
	p := s.permOf(agent.ToolCall{ID: "b1", Name: "bash", Input: in})
	if p.Summary != "go mod init wordfreq" {
		t.Fatalf("summary = %q", p.Summary)
	}
	long := strings.Repeat("x", 70) + " && go test ./... && go vet ./..."
	in, _ = json.Marshal(map[string]string{"command": long})
	p = s.permOf(agent.ToolCall{ID: "b2", Name: "bash", Input: in})
	if p.Preview != long {
		t.Fatalf("long command not previewed whole: %q", p.Preview)
	}
	if got := shell.TrimWorkspaceCd("cd /elsewhere && make", cwd); got != "cd /elsewhere && make" {
		t.Fatalf("a cd somewhere else is kept: %q", got)
	}
}

// TestPlanToolSummaryIsNotRawJSON: the todo tool's row read
// `{"todos": [{"text": "Create Go module…` in the thread.
func TestPlanToolSummaryIsNotRawJSON(t *testing.T) {
	raw := `{"todos":[{"text":"Create module","status":"completed"},{"text":"Write tests","status":"in_progress"},{"text":"Run","status":"pending"}]}`
	if got := present.ToolSummary("todo", raw, 80, ""); got != "3 steps · Write tests" {
		t.Fatalf("summary = %q", got)
	}
}

// TestDroppedPlaceholderTellsThePage: an empty thinking placeholder the
// server drops when the next block starts used to vanish only from the
// server's thread, so the page kept a "Thinking…" row above the rest of
// the turn for minutes. The drop is sent, so the page can remove it.
func TestDroppedPlaceholderTellsThePage(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.apply(agent.Event{Kind: agent.EvThinking, Text: ""})
	s.mu.Lock()
	id := s.entries[len(s.entries)-1].ID
	s.mu.Unlock()
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: &agent.ToolCall{ID: "c1", Name: "bash"}})
	s.apply(agent.Event{Kind: agent.EvText, Text: "partial"})
	s.apply(agent.Event{Kind: agent.EvDiscard})
	s.mu.Lock()
	defer s.mu.Unlock()
	gone := map[string]bool{}
	for _, e := range s.log {
		if e.Type == "entry" && e.Entry != nil && e.Entry.Gone {
			gone[e.Entry.Kind] = true
			if e.Entry.Kind == "thinking" && e.Entry.ID != id {
				t.Fatalf("dropped id %q, want %q", e.Entry.ID, id)
			}
		}
	}
	if !gone["thinking"] || !gone["assistant"] {
		t.Fatalf("drops sent: %v; want the thinking placeholder and the discarded text", gone)
	}
	for _, e := range s.entries {
		if e.ID == id {
			t.Fatal("the placeholder is still in the thread")
		}
	}
}

// TestFailedTurnClosesItsOpenRows: a turn that ended in an error (a 401,
// a 429 that ran out of retries) left its "thinking" placeholder live
// above the error card, still reading "retrying 2/2 · in 3s".
func TestFailedTurnClosesItsOpenRows(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.apply(agent.Event{Kind: agent.EvThinking, Text: ""})
	s.apply(agent.Event{Kind: agent.EvText, Text: "half an ans"})
	s.finish(errors.New("Unauthorized (HTTP 401)"))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if (e.Kind == "thinking" || e.Kind == "assistant") && !e.Done {
			t.Fatalf("a %s row is still open after the turn failed: %+v", e.Kind, *e)
		}
		if e.Kind == "thinking" {
			t.Fatalf("the empty placeholder was kept: %+v", *e)
		}
	}
}

// TestWebProviderErrorSaysWhatToDo: the page showed "Unauthorized (HTTP
// 401)" and nothing else, while the terminal said which key to check and
// how. It shows the same headline, reason and next step now, names the
// provider the way people do, and offers the connect form as a button
// rather than naming /connect.
func TestWebProviderErrorSaysWhatToDo(t *testing.T) {
	s := newServer(context.Background(), Options{Provider: "openrouter"})
	s.finish(errors.New("Unauthorized (HTTP 401)"))
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.entries[len(s.entries)-1]
	if last.Kind != "error" || !strings.HasPrefix(last.Text, "Authentication failed (HTTP 401): Unauthorized.") ||
		!strings.Contains(last.Text, "Check the API key for OpenRouter in the connection settings.") || strings.Contains(last.Text, "/connect") {
		t.Fatalf("error row = %q", last.Text)
	}
	// The page lays the parts out itself, and offers no retry: the same
	// key fails the same way until it is changed.
	x, ok := last.Data.(errorCard)
	if !ok || x.Headline != "Authentication failed" || x.Retry || x.Fix != "connect" {
		t.Fatalf("error data = %#v", last.Data)
	}
	if s.status != "turn failed" {
		t.Fatalf("status line = %q, want it not to repeat the card's headline", s.status)
	}
}

// TestRetryWaitCountsDown: "retrying 2/2 · rate limited · in 3s" sat still
// on the page while the turn's clock ran past it ("in 3s · 4.5s"). The
// wait counts down from when the phase was set, as it does in the TUI.
func TestRetryWaitCountsDown(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	s.setPhase("retrying 2/2 · rate limited · in 3s")
	if got := s.statusNow().Phase; got != "retrying 2/2 · rate limited · in 3s" {
		t.Fatalf("fresh = %q", got)
	}
	s.mu.Lock()
	s.phaseSince = s.phaseSince.Add(-2100 * time.Millisecond)
	s.mu.Unlock()
	if got := s.statusNow().Phase; got != "retrying 2/2 · rate limited · in 1s" {
		t.Fatalf("after 2.1s = %q", got)
	}
	// Setting the same phase again does not restart the clock.
	s.setPhase("retrying 2/2 · rate limited · in 3s")
	if got := s.statusNow().Phase; got != "retrying 2/2 · rate limited · in 1s" {
		t.Fatalf("re-sent = %q", got)
	}
}

// TestAdjacentThoughtsMerge: steps with nothing between them but a
// model's stray "." left four "Thought" rows stacked on the page, each
// opening "The user asked me to…". A thought that follows a thought is
// the same thought going on.
func TestAdjacentThoughtsMerge(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.apply(agent.Event{Kind: agent.EvThinking, Text: "first idea"})
	s.apply(agent.Event{Kind: agent.EvText, Text: ".\n\n"})
	s.apply(agent.Event{Kind: agent.EvTextBlockDone})
	s.apply(agent.Event{Kind: agent.EvThinking, Text: "second idea"})
	s.apply(agent.Event{Kind: agent.EvTextBlockDone})
	s.mu.Lock()
	var thoughts []entry
	for _, e := range s.entries {
		if e.Kind == "thinking" {
			thoughts = append(thoughts, *e)
		}
		if e.Kind == "assistant" {
			t.Fatalf("the stray %q was kept between thoughts", e.Text)
		}
	}
	s.mu.Unlock()
	if len(thoughts) != 1 || thoughts[0].Text != "first idea\n\nsecond idea" {
		t.Fatalf("thoughts = %+v", thoughts)
	}
	// A tool call between two thoughts keeps them apart: the order of
	// thinking and doing is the story of the turn.
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: &agent.ToolCall{ID: "c1", Name: "bash"}})
	s.apply(agent.Event{Kind: agent.EvThinking, Text: "after the tool"})
	s.mu.Lock()
	defer s.mu.Unlock()
	if last := s.entries[len(s.entries)-1]; last.Kind != "thinking" || last.Text != "after the tool" {
		t.Fatalf("last = %+v", *last)
	}
}

// TestFailingCommandRowSaysSo: the bash tool reports a non-zero exit in
// its output, so a red test run showed "Done" with "[exit code 1]" at the
// bottom of its output. The row says Failed and the exit code; the marker
// line, which is for the model, is not shown.
func TestFailingCommandRowSaysSo(t *testing.T) {
	s := newServer(context.Background(), Options{})
	call := &agent.ToolCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"pytest -q"}`)}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	s.apply(agent.Event{Kind: agent.EvToolResult, Call: call, Output: "3 failed, 2 passed\n[exit code 1]"})
	grep := &agent.ToolCall{ID: "c2", Name: "bash", Input: json.RawMessage(`{"command":"grep -n nope a.go"}`)}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: grep})
	s.apply(agent.Event{Kind: agent.EvToolResult, Call: grep, Output: "\n[exit code 1] no lines matched; for this command that is an answer, not a failure"})
	s.mu.Lock()
	defer s.mu.Unlock()
	red, none := s.entries[len(s.entries)-2], s.entries[len(s.entries)-1]
	if !red.Error || red.Output != "3 failed, 2 passed" || !strings.HasPrefix(red.Dur, "exit 1") {
		t.Fatalf("red run = %+v", *red)
	}
	if none.Error || none.Output != "no lines matched" {
		t.Fatalf("a search that found nothing is not a failure: %+v", *none)
	}
}

// An MCP call whose arguments are not a JSON object was summarised as
// the raw arguments: a bare token string went to the browser verbatim.
func TestMCPSummaryNeverQuotesNonObjectArguments(t *testing.T) {
	for raw, want := range map[string]string{
		`"sk-live-abc123"`:                        "(json string)",
		`["sk-live-abc123"]`:                      "(json array)",
		`sk-live-abc123 not json`:                 "(arguments are not valid JSON)",
		`{"api_key":"sk-live-abc123","repo":"x"}`: "{api_key, repo}",
	} {
		got := present.ToolSummary("mcp__github__call", raw, 200, "/p")
		if got != want || strings.Contains(got, "sk-live") {
			t.Errorf("%s → %q, want %q", raw, got, want)
		}
	}
}

// The terminal said when a /goal ended and coloured a failed verdict red;
// the page showed a faint tool row and a grey verdict.
func TestGoalEndAndVerdictToneMatchTheTerminal(t *testing.T) {
	s := newServer(context.Background(), Options{Cwd: "/work/proj"})
	call := &agent.ToolCall{ID: "g1", Name: "goal_status", Input: json.RawMessage(`{"status":"complete"}`)}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	s.apply(agent.Event{Kind: agent.EvToolResult, Call: call, Output: "goal g-1 complete: the CLI ships\n"})
	s.apply(agent.Event{Kind: agent.EvVerdict, Text: "✗ tests fail: /work/proj/cli_test.go", IsError: true})
	s.mu.Lock()
	defer s.mu.Unlock()
	var goal, verdict *entry
	for _, e := range s.entries {
		if e.Kind == "verdict" && strings.HasPrefix(e.Text, "◎") {
			goal = e
		} else if e.Kind == "verdict" {
			verdict = e
		}
	}
	if goal == nil || !strings.Contains(goal.Text, "goal g-1 complete: the CLI ships · nothing more runs for it") {
		t.Fatalf("goal line = %+v", goal)
	}
	if verdict == nil || verdict.Tone != present.ToneBad || verdict.Text != "✗ tests fail: cli_test.go" {
		t.Fatalf("verdict = %+v", verdict)
	}
}

// The status line names what runs the same way the terminal does, and
// says the model is thinking when it is.
func TestPhaseWordingMatchesTheTerminal(t *testing.T) {
	s := newServer(context.Background(), Options{Cwd: "/p"})
	phase := func() string { s.mu.Lock(); defer s.mu.Unlock(); return s.phase }
	s.apply(agent.Event{Kind: agent.EvThinking, Text: "hm"})
	if got := phase(); got != present.PhaseThinking {
		t.Fatalf("thinking phase = %q", got)
	}
	call := &agent.ToolCall{ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"cd /p && go test ./..."}`)}
	s.apply(agent.Event{Kind: agent.EvToolStart, Call: call})
	s.apply(agent.Event{Kind: agent.EvToolRun, Call: call})
	if got := phase(); got != "bash go test ./..." {
		t.Fatalf("running phase = %q", got)
	}
}

// A stray "." between tool calls is dropped when it closes, as the
// terminal drops it; the page no longer has to hide it.
func TestPunctuationOnlyAnswerIsDropped(t *testing.T) {
	s := newServer(context.Background(), Options{})
	s.apply(agent.Event{Kind: agent.EvText, Text: "."})
	s.apply(agent.Event{Kind: agent.EvTextBlockDone})
	s.apply(agent.Event{Kind: agent.EvText, Text: "Done: 3 files."})
	s.apply(agent.Event{Kind: agent.EvTextBlockDone})
	s.mu.Lock()
	defer s.mu.Unlock()
	var texts []string
	for _, e := range s.entries {
		if e.Kind == "assistant" {
			texts = append(texts, e.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "Done: 3 files." {
		t.Fatalf("answers = %q", texts)
	}
}

// A reopened session draws its tool rows the way the live ones were: a
// read of a file that did not exist is an answer, not a red failure.
func TestReplayedMissingReadIsNotAFailure(t *testing.T) {
	s := newServer(context.Background(), Options{Agent: &agent.Agent{Tools: tool.NewRegistry()}})
	s.replayHistory([]llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "r1", Name: "read_file", Input: json.RawMessage(`{"path":"x_test.go"}`)}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "r1", IsError: true, Text: "Error: x_test.go does not exist"}}},
	})
	_, ents, _, _, _ := s.snapshot()
	for _, e := range ents {
		if e.Kind == "tool" && (e.Error || e.Output != "x_test.go does not exist") {
			t.Fatalf("replayed row = %+v", e)
		}
	}
}
