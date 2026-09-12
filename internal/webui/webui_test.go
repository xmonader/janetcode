package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

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
	}
	h := Handler(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test"})
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
	for _, want := range []string{"New", "Plan", "Changed files", "Background jobs", "Subagents", "id=\"sessions\"", "id=\"composer\"", "id=\"mode-chip\"", "id=\"questions\"", "id=\"mode-auto\""} {
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
