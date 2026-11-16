package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// SecretEnvPrefix starts the name of every variable a requested secret is
// exported under.
const SecretEnvPrefix = "JANETCODE_SECRET_"

// SecretRequest is what a front-end shows in its private prompt. It has
// the model's words only; the value comes back through SecretReply and
// goes nowhere else.
type SecretRequest struct {
	Name    string // what the model called it, as given
	EnvVar  string // the variable it will be exported as
	Purpose string // why the model needs it
	Usage   string // how the model will use it
}

// SecretReply is the user's answer to a SecretRequest.
type SecretReply struct {
	Value    string
	Declined bool
}

// SecretAsker is an Asker that can also take a secret in a private
// prompt: masked on screen, never echoed into the transcript, input
// history or any event. An Asker without it (a headless or background
// run, ACP) cannot be asked for one, and the tool refuses.
type SecretAsker interface {
	AskSecret(ctx context.Context, req SecretRequest) (SecretReply, error)
}

// SecretEnvName is the variable a secret called name is exported as:
// upper-case letters, digits and underscores, so any name the model picks
// is a valid shell variable. It is empty when nothing usable is left.
func SecretEnvName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(name)) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "_")
	if s == "" {
		return ""
	}
	return SecretEnvPrefix + s
}

// RequestSecretTool lets the model ask the user for a credential without
// the value ever entering the conversation.
type RequestSecretTool struct{ Parent *Agent }

func (*RequestSecretTool) Name() string { return "request_secret" }
func (*RequestSecretTool) Description() string {
	return "Ask the user for a secret you need to run a command (an API token, a password) in a private prompt. " +
		"Never ask for one in chat and never ask the user to paste it into a file: the value does not reach you. " +
		"If the user supplies it, it is exported to your bash commands for the rest of the session as $" + SecretEnvPrefix + "<NAME>, " +
		"and any output that contains it is redacted. Use it by variable name only (curl -H \"Authorization: Bearer $" + SecretEnvPrefix + "GITHUB_TOKEN\" ...). " +
		"Explain what it is for and how you will use it: the user reads both before answering."
}
func (*RequestSecretTool) Mutating() bool { return false }

// ParentOnly keeps the tool off subagents: they have no user to ask.
func (*RequestSecretTool) ParentOnly() bool { return true }
func (*RequestSecretTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"name":{"type":"string","description":"short name of the secret, e.g. GITHUB_TOKEN; exported as $` + SecretEnvPrefix + `<NAME>"},
"purpose":{"type":"string","description":"what you need it for"},
"usage":{"type":"string","description":"how you will use it, e.g. which command"}},
"required":["name","purpose","usage"],"additionalProperties":false}`)
}

func (t *RequestSecretTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Name    string `json:"name"`
		Purpose string `json:"purpose"`
		Usage   string `json:"usage"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	env := SecretEnvName(in.Name)
	if env == "" {
		return "", fmt.Errorf("request_secret needs a name made of letters or digits, like GITHUB_TOKEN")
	}
	if strings.TrimSpace(in.Purpose) == "" || strings.TrimSpace(in.Usage) == "" {
		return "", fmt.Errorf("request_secret needs a purpose and a usage: the user reads both before they answer")
	}
	if t.Parent != nil && t.Parent.Away() {
		return "", fmt.Errorf("the user is away and cannot be asked for a secret. Continue without it, or say what is blocked")
	}
	var sa SecretAsker
	if t.Parent != nil {
		sa, _ = t.Parent.Asker.(SecretAsker)
	}
	if sa == nil {
		return "", fmt.Errorf("this session has no private prompt for secrets (it is not interactive). Do not ask for the value in text; tell the user which variable you need, or use an op:// reference in the config")
	}
	reply, err := sa.AskSecret(ctx, SecretRequest{
		Name:    strings.TrimSpace(in.Name),
		EnvVar:  env,
		Purpose: strings.TrimSpace(in.Purpose),
		Usage:   strings.TrimSpace(in.Usage),
	})
	if err != nil {
		return "", fmt.Errorf("secret prompt failed: %w", err)
	}
	if reply.Declined || reply.Value == "" {
		return "The user declined to provide the secret.", nil
	}
	// Redaction is registered before the variable exists, so there is no
	// moment a command could print a value nothing would take out.
	if !secret.Register(reply.Value) {
		return "", fmt.Errorf("the value the user entered is too short to be a secret (under 4 characters); it was not saved")
	}
	shell.SetSessionSecret(env, reply.Value)
	return fmt.Sprintf("The secret %q is available to your bash commands as $%s for the rest of this session. Reference it by variable name only; any output containing its value is redacted.", in.Name, env), nil
}

var (
	_ tool.Tool  = (*RequestSecretTool)(nil)
	_ ParentOnly = (*RequestSecretTool)(nil)
)
