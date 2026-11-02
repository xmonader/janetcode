package effbench

import (
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, url, body string, hdr map[string]string) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestProxyOpenAIStream(t *testing.T) {
	var gotAuth, gotBody string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotAuth, gotBody = r.Header.Get("Authorization"), string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"model\":\"kimi-k2\",\"choices\":[]}\n\n")
		io.WriteString(w, "data: {\"model\":\"kimi-k2\",\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":7,\"prompt_tokens_details\":{\"cached_tokens\":60}}}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	var buf bytes.Buffer
	lg := NewLog(&buf)
	px := httptest.NewServer(&Proxy{Upstream: up.URL + "/v1", Key: "REAL", Log: lg})
	defer px.Close()
	out := post(t, px.URL+"/v1/chat/completions", `{"model":"deepseek-v4","stream":true}`, map[string]string{"Authorization": "Bearer dummy"})
	if !strings.Contains(out, "[DONE]") {
		t.Fatalf("stream not passed through: %q", out)
	}
	if gotAuth != "Bearer REAL" || !strings.Contains(gotBody, "include_usage") {
		t.Fatalf("auth %q body %q", gotAuth, gotBody)
	}
	r := lg.Records()[0]
	if r.Input != 100 || r.Cached != 60 || r.Fresh != 40 || r.Output != 7 || r.Served != "kimi-k2" || r.Asked != "deepseek-v4" || !r.Mismatch || r.Status != 200 {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(buf.String(), `"mismatch":true`) {
		t.Fatalf("jsonl: %s", buf.String())
	}
}

func TestProxyAnthropicJSONAndStream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "K" {
			http.Error(w, "bad", 400)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-5\",\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":90,\"cache_creation_input_tokens\":5,\"output_tokens\":1}}}\n\n")
			io.WriteString(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":42}}\n\n")
			return
		}
		io.WriteString(w, `{"model":"claude-sonnet-5","usage":{"input_tokens":3,"output_tokens":4}}`)
	}))
	defer up.Close()
	lg := NewLog(nil)
	px := httptest.NewServer(&Proxy{Upstream: up.URL, Key: "K", Log: lg})
	defer px.Close()
	post(t, px.URL+"/v1/messages", `{"model":"claude-sonnet-5"}`, nil)
	post(t, px.URL+"/v1/messages", `{"model":"claude-sonnet-5","stream":true}`, nil)
	rs := lg.Records()
	if len(rs) != 2 || rs[0].API != "anthropic" || rs[0].Input != 3 || rs[0].Output != 4 {
		t.Fatalf("%+v", rs)
	}
	s := rs[1]
	if s.Input != 105 || s.Cached != 90 || s.Fresh != 15 || s.CacheWrite != 5 || s.Output != 42 || s.Mismatch {
		t.Fatalf("%+v", s)
	}
}

func TestMismatched(t *testing.T) {
	for _, c := range []struct {
		a, s string
		want bool
	}{
		{"claude-sonnet-4-5", "anthropic/claude-sonnet-4.5", false},
		{"deepseek-v4", "kimi-k2", true},
		{"deepseek-v4", "", false},
		{"my-model", "kimi-k2", false},
	} {
		if got := Mismatched(c.a, c.s); got != c.want {
			t.Errorf("%s/%s = %v", c.a, c.s, got)
		}
	}
}

func TestAdjustedSeconds(t *testing.T) {
	// 100s wall, 40s decoding 400 tokens (10 tok/s). At 20 tok/s decode takes 20s.
	tot := Totals{Output: 400, DecodeMS: 40000}
	if got := AdjustedSeconds(100, tot, 20); math.Abs(got-80) > 1e-9 {
		t.Fatalf("got %v", got)
	}
	if AdjustedSeconds(100, Totals{}, 20) != 100 || AdjustedSeconds(100, tot, 0) != 100 {
		t.Fatal("no speed must leave wall unchanged")
	}
}

func TestAdapterCommand(t *testing.T) {
	var a Adapter
	for _, x := range Builtin() {
		if x.Name == "janetcode" {
			a = x
		}
	}
	argv, env, _ := a.Command(Vars{BaseURL: a.BaseURL("http://127.0.0.1:1"), Model: "m", Prompt: "do it", Key: "k"})
	if strings.Join(argv, " ") != "janetcode -p do it --provider openai --model m --yolo" {
		t.Fatalf("%v", argv)
	}
	if !strings.Contains(strings.Join(env, " "), "OPENAI_BASE_URL=http://127.0.0.1:1/v1") {
		t.Fatalf("%v", env)
	}
	if (Adapter{Bin: "definitely-not-installed-xyz"}).Bin == "" {
		t.Fatal()
	}
	if _, ok := (Adapter{Bin: "definitely-not-installed-xyz"}).Available(); ok {
		t.Fatal("missing binary must be unavailable")
	}
}

func TestUpstreamPolicy(t *testing.T) {
	t.Setenv("AIBROKER_AI02_API_KEY", "")
	if _, _, err := UpstreamFromEnv(); err == nil {
		t.Fatal("missing key must fail")
	}
	t.Setenv("AIBROKER_AI02_API_KEY", "k")
	t.Setenv("AIBROKER_AI02_BASE_URL", "https://openrouter.ai/api")
	if _, _, err := UpstreamFromEnv(); err == nil {
		t.Fatal("openrouter must be refused")
	}
	t.Setenv("AIBROKER_AI02_BASE_URL", "")
	if b, _, err := UpstreamFromEnv(); err != nil || b != DefaultUpstream {
		t.Fatal(b, err)
	}
}

func TestSelfTestAndReport(t *testing.T) {
	var rep bytes.Buffer
	if err := SelfTest(context.Background(), t.TempDir(), &rep); err != nil {
		t.Fatal(err)
	}
	s := rep.String()
	for _, want := range []string{"| fake-agent | 1/1 |", "| swapper | 1/1 |", "MISMATCH", "deepseek/deepseek-v4", "80%"} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q:\n%s", want, s)
		}
	}
}
