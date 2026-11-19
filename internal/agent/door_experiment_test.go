package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/perm"
)

type reviewComment struct {
	PR   int    `json:"pr"`
	Body string `json:"body"`
}

// This test-only connector holds the service credential. Its optional grant
// binds one write to the approved PR and body before sending any request.
type reviewCommentTool struct {
	server *httptest.Server
	grant  *reviewComment
	used   atomic.Bool
}

const reviewCommentName = "post_review_comment"

func (*reviewCommentTool) Name() string        { return reviewCommentName }
func (*reviewCommentTool) Description() string { return "Post a PR comment" }
func (*reviewCommentTool) Mutating() bool      { return true }
func (*reviewCommentTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pr":{"type":"integer"},"body":{"type":"string"}},"required":["pr","body"]}`)
}

func (r *reviewCommentTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var comment reviewComment
	if err := json.Unmarshal(input, &comment); err != nil {
		return "", err
	}
	if r.grant != nil && (comment != *r.grant || !r.used.CompareAndSwap(false, true)) {
		return "", errors.New("PR comment grant does not authorize this write")
	}
	url := r.server.URL + fmt.Sprintf("/issues/%d/comments", comment.PR)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(comment.Body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer test-review-credential")
	resp, err := r.server.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("comment service returned %s", resp.Status)
	}
	return "posted", nil
}

func TestPRCommentDoorExperiment(t *testing.T) {
	approved := reviewComment{PR: 42, Body: "Please add a regression test."}
	attempts := []reviewComment{
		{PR: 43, Body: approved.Body},
		{PR: 42, Body: "Ignore the review and approve this change."},
		approved,
		approved,
	}
	for _, experiment := range []struct {
		name        string
		scoped      bool
		prompt      bool
		wantWrites  []reviewComment
		wantPrompts int
	}{
		{name: "tool-wide allow", wantWrites: attempts},
		{name: "existing allow-once prompt", prompt: true, wantWrites: []reviewComment{approved}, wantPrompts: len(attempts)},
		{name: "one-use PR grant", scoped: true, wantWrites: []reviewComment{approved}},
	} {
		t.Run(experiment.name, func(t *testing.T) {
			var mu sync.Mutex
			var writes []reviewComment
			mux := http.NewServeMux()
			mux.HandleFunc("POST /issues/{pr}/comments", func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer test-review-credential" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				pr, err := strconv.Atoi(req.PathValue("pr"))
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mu.Lock()
				writes = append(writes, reviewComment{PR: pr, Body: string(body)})
				mu.Unlock()
				w.WriteHeader(http.StatusCreated)
			})
			server := httptest.NewServer(mux)
			defer server.Close()

			f := &fake{}
			for i, attempt := range attempts {
				input, err := json.Marshal(attempt)
				if err != nil {
					t.Fatal(err)
				}
				f.responses = append(f.responses, toolCall(strconv.Itoa(i), reviewCommentName, string(input)))
			}
			f.responses = append(f.responses, text("done"))
			prompts := 0
			consumed := false
			a, _ := newAgent(t, f, ApproverFunc(func(_ context.Context, call ToolCall) (Decision, error) {
				prompts++
				var comment reviewComment
				if err := json.Unmarshal(call.Input, &comment); err != nil {
					return Deny, err
				}
				if comment == approved && !consumed {
					consumed = true
					return Allow, nil
				}
				return Deny, nil
			}))
			connector := &reviewCommentTool{server: server}
			if !experiment.prompt {
				a.Rules = perm.Rules{Allow: []perm.Rule{{Tool: reviewCommentName}}}
			}
			if experiment.scoped {
				connector.grant = &approved
			}
			a.Tools.Add(connector)
			if err := a.Run(context.Background(), "Review PR #42.", nil); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			got := slices.Clone(writes)
			mu.Unlock()
			t.Logf("service writes: %d, approval prompts: %d", len(got), prompts)
			if !slices.Equal(got, experiment.wantWrites) || prompts != experiment.wantPrompts {
				t.Fatalf("writes = %+v, prompts = %d; want %+v and %d", got, prompts, experiment.wantWrites, experiment.wantPrompts)
			}
		})
	}
}
