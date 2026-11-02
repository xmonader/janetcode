package effbench

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
)

// FakeUpstream is an OpenAI-compatible server answering with a stream that
// carries usage. served is the model name it reports back.
func FakeUpstream(served string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, l := range []string{
			`{"model":"` + served + `","choices":[{"delta":{"content":"ok"}}]}`,
			`{"model":"` + served + `","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"prompt_tokens_details":{"cached_tokens":800}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", l)
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(20 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// SelfTest runs the whole pipeline (proxy, workspace, grading, report) with a
// stand-in agent against a fake upstream, writing under out and the report to
// rep. No network, no credentials, no real agent. The fake upstream answers as
// a different vendor for the "swapper" arm so the mismatch flag is exercised.
func SelfTest(ctx context.Context, out string, rep io.Writer) error {
	good, swap := FakeUpstream("deepseek/deepseek-v4"), FakeUpstream("moonshot/kimi-k2")
	defer good.Close()
	defer swap.Close()
	task := eval.Task{Name: "st-01-create-file", Prompt: "create out.txt", Check: "test -f out.txt"}
	var buf bytes.Buffer
	for _, arm := range []struct{ name, up string }{{"fake-agent", good.URL}, {"swapper", swap.URL}} {
		cfg := &Config{Out: out, Campaign: "selftest", Model: "deepseek-v4", Seeds: 1, Timeout: time.Minute,
			Adapters: []Adapter{{Name: arm.name, Bin: "fake", Proto: "openai"}}, Tasks: []eval.Task{task},
			Upstream: arm.up, Key: "selftest-key",
			RunnerHook: func(ctx context.Context, a Adapter, v Vars, ws string) error {
				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, v.BaseURL+"/chat/completions",
					bytes.NewReader([]byte(`{"model":"`+v.Model+`","stream":true,"messages":[]}`)))
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				defer resp.Body.Close()
				_, _ = io.Copy(io.Discard, resp.Body)
				return os.WriteFile(filepath.Join(ws, "out.txt"), []byte("hi"), 0o644)
			}}
		res, err := cfg.Execute(ctx)
		if err != nil {
			return err
		}
		for _, r := range res {
			if !r.Pass {
				return fmt.Errorf("selftest: %s did not pass: %s", r.Arm, r.Error)
			}
		}
	}
	runs, err := LoadRuns(filepath.Join(out, "selftest"))
	if err != nil {
		return err
	}
	if len(runs) != 2 {
		return fmt.Errorf("selftest: want 2 runs, found %d", len(runs))
	}
	WriteReport(&buf, runs, "fake-agent", 0)
	if !bytes.Contains(buf.Bytes(), []byte("MISMATCH")) {
		return fmt.Errorf("selftest: swapped model was not flagged")
	}
	_, err = rep.Write(buf.Bytes())
	return err
}
