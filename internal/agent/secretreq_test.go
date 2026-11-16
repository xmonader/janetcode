package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

type secretAsker struct {
	AskFunc
	reply SecretReply
	got   []SecretRequest
}

func (s *secretAsker) AskSecret(_ context.Context, r SecretRequest) (SecretReply, error) {
	s.got = append(s.got, r)
	return s.reply, nil
}

func secretCleanup(t *testing.T) {
	t.Cleanup(func() { secret.Forget(); shell.ClearSessionSecrets() })
}

const secretInput = `{"name":"deploy token","purpose":"push the release","usage":"curl -H Authorization"}`

// The whole path: the model asks, the user answers in the private prompt,
// the model's bash command reads the variable and prints it, and neither
// the model's context, the events nor the session file on disk ever hold
// the value.
func TestRequestSecretValueNeverLeavesTheProcess(t *testing.T) {
	secretCleanup(t)
	const value = "s3cr3t-Value/with spaces 42"
	f := &fake{responses: []*llm.Response{
		toolCall("c1", "request_secret", secretInput),
		toolCall("c2", "bash", `{"command":"printf '%s|%s' \"$JANETCODE_SECRET_DEPLOY_TOKEN\" \"${#JANETCODE_SECRET_DEPLOY_TOKEN}\""}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	a.Tools.Add(&RequestSecretTool{Parent: a})
	asker := &secretAsker{reply: SecretReply{Value: value}}
	a.Asker = asker
	dir := t.TempDir()
	sess, err := session.New(dir, root, "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil {
		t.Fatal(err)
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "deploy", emit); err != nil {
		t.Fatal(err)
	}
	if len(asker.got) != 1 || asker.got[0].EnvVar != "JANETCODE_SECRET_DEPLOY_TOKEN" || asker.got[0].Purpose != "push the release" {
		t.Fatalf("request = %+v", asker.got)
	}
	// The command really ran with the variable: the length proves it was
	// set, the redaction proves nothing printed the value.
	var bashOut string
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && b.ToolUseID == "c2" {
				bashOut = b.Text
			}
		}
	}
	if !strings.Contains(bashOut, "[redacted]|"+"27") {
		t.Fatalf("bash output = %q, want redacted value and length 27", bashOut)
	}
	assertNoValue(t, value, a, evs, sess.Path())
}

func assertNoValue(t *testing.T, value string, a *Agent, evs *[]Event, sessPath string) {
	t.Helper()
	b, _ := json.Marshal(a.Messages)
	if strings.Contains(string(b), value) {
		t.Fatal("value is in the conversation history")
	}
	b, _ = json.Marshal(*evs)
	if strings.Contains(string(b), value) {
		t.Fatal("value is in an emitted event")
	}
	disk, err := os.ReadFile(sessPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk) == 0 || strings.Contains(string(disk), value) {
		t.Fatalf("session file empty or holds the value (%d bytes)", len(disk))
	}
}

func TestRequestSecretDeclineIsPlain(t *testing.T) {
	secretCleanup(t)
	a := &Agent{}
	a.Asker = &secretAsker{reply: SecretReply{Declined: true}}
	out, err := (&RequestSecretTool{Parent: a}).Run(context.Background(), json.RawMessage(secretInput))
	if err != nil || out != "The user declined to provide the secret." {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if n := shell.SessionSecretNames(); len(n) != 0 {
		t.Fatalf("declined request exported %v", n)
	}
}

func TestRequestSecretRefusesWithoutAPrivatePrompt(t *testing.T) {
	secretCleanup(t)
	plain := &Agent{}
	plain.Asker = AskFunc(func(context.Context, []Question) ([]Answer, error) {
		t.Fatal("a question asker must not be used for a secret")
		return nil, nil
	})
	for name, a := range map[string]*Agent{"no parent asker": {}, "plain asker": plain} {
		_, err := (&RequestSecretTool{Parent: a}).Run(context.Background(), json.RawMessage(secretInput))
		if err == nil || !strings.Contains(err.Error(), "not interactive") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// Nobody at all: still a clear error, not a hang or a panic.
	if _, err := (&RequestSecretTool{}).Run(context.Background(), json.RawMessage(secretInput)); err == nil {
		t.Fatal("nil parent accepted")
	}
}

func TestRequestSecretRefusedWhileAway(t *testing.T) {
	secretCleanup(t)
	a := &Agent{}
	a.Asker = &secretAsker{reply: SecretReply{Value: "valuevalue"}}
	if _, err := a.SetAway(true); err != nil {
		t.Skipf("away unavailable here: %v", err)
	}
	_, err := (&RequestSecretTool{Parent: a}).Run(context.Background(), json.RawMessage(secretInput))
	if err == nil || !strings.Contains(err.Error(), "away") {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestSecretRejectsBadInputAndShortValues(t *testing.T) {
	secretCleanup(t)
	a := &Agent{}
	a.Asker = &secretAsker{reply: SecretReply{Value: "abc"}}
	tl := &RequestSecretTool{Parent: a}
	for _, in := range []string{`{`, `{"name":"!!!","purpose":"p","usage":"u"}`, `{"name":"X","purpose":"","usage":"u"}`, secretInput} {
		if _, err := tl.Run(context.Background(), json.RawMessage(in)); err == nil {
			t.Errorf("accepted %s", in)
		}
	}
	if n := shell.SessionSecretNames(); len(n) != 0 {
		t.Fatalf("exported %v", n)
	}
}

func TestSecretEnvName(t *testing.T) {
	for in, want := range map[string]string{
		"GITHUB_TOKEN": "JANETCODE_SECRET_GITHUB_TOKEN",
		" deploy-key ": "JANETCODE_SECRET_DEPLOY_KEY",
		"a.b c":        "JANETCODE_SECRET_A_B_C",
		"$$":           "",
	} {
		if got := SecretEnvName(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
