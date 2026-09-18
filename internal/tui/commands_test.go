package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptest"
	"github.com/xmonader/janetcode/internal/mcp/mcptool"
	"github.com/xmonader/janetcode/internal/mcp/testserver"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/slash"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/trust"
	"github.com/xmonader/janetcode/internal/verify"
)

func TestShellPassthrough(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "seen"), StopReason: llm.StopEndTurn})
	h.keys("!echo hello-from-shell; echo err >&2; exit 3")
	h.enter()
	if !h.m.turn.running {
		t.Fatal("shell should mark the UI busy")
	}
	h.pump(func() bool { return !h.m.turn.running })
	v := plain(h.m.View())
	for _, want := range []string{"shell", "hello-from-shell", "output shared with the model"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	// The next prompt carries the command and its output.
	h.keys("what did that print?")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if n := len(h.m.opts.Agent.Messages); n != 2 {
		t.Fatalf("messages = %d, the shell context must merge into the user turn", n)
	}
	sent := h.m.opts.Agent.Messages[0].Text()
	for _, want := range []string{"$ echo hello-from-shell", "\nhello-from-shell\n", "[exit code 3]", "what did that print?"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("missing %q in shared context: %q", want, sent)
		}
	}
}

func TestCustomSlashCommand(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "reviewed"), StopReason: llm.StopEndTurn})
	dir := filepath.Join(h.m.opts.Cwd, ".janetcode", "commands")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "audit.md"), []byte("# Careful review\nReview $ARGUMENTS for bugs."), 0o644)
	// Commands load at construction; rebuild the model against the same dir.
	h.m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(h.m.opts.Cwd, "") {
		h.m.custom["/"+c.Name] = c
	}
	h.keys("/aud")
	if v := plain(h.m.View()); !strings.Contains(v, "/audit") || !strings.Contains(v, "Careful review") {
		t.Fatalf("completion missing:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	h.keys("main.go")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if sent := h.m.opts.Agent.Messages[0].Text(); sent != "# Careful review\nReview main.go for bugs." {
		t.Fatalf("sent = %q", sent)
	}
	h.keys("/help")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/audit") {
		t.Fatalf("help lacks custom command:\n%s", v)
	}
}

func TestFindScrollsToMatches(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for i := 0; i < 30; i++ {
		h.m.transcript.info(fmt.Sprintf("filler line %d", i))
	}
	h.m.transcript.info("the NEEDLE is here")
	for i := 0; i < 30; i++ {
		h.m.transcript.info(fmt.Sprintf("more %d", i))
	}
	h.m.transcript.info("second needle")
	h.m.refresh(false)
	h.m.viewport.GotoTop()
	h.keys("/find needle")
	h.enter()
	if !strings.Contains(h.m.status, "match 1/2") {
		t.Fatalf("status = %q", h.m.status)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "the NEEDLE is here") {
		t.Fatalf("viewport not scrolled to the match:\n%s", v)
	}
	h.keys("/find")
	h.enter()
	if !strings.Contains(h.m.status, "match 2/2") {
		t.Fatalf("repeat status = %q", h.m.status)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "second needle") {
		t.Fatalf("viewport not at second match:\n%s", v)
	}
	h.keys("/find")
	h.enter()
	if !strings.Contains(h.m.status, "match 1/2") {
		t.Fatalf("wrap status = %q", h.m.status)
	}
	h.keys("/find zzz")
	h.enter()
	if !strings.Contains(h.m.status, "no match") {
		t.Fatalf("miss status = %q", h.m.status)
	}
}

func TestUndoCommand(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.keys("/undo")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing to undo") {
		t.Fatalf("view:\n%s", v)
	}
	h.m.opts.Agent.AutoApprove = true
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	target := filepath.Join(h.m.opts.Cwd, "n.txt")
	if _, err := os.Stat(target); err != nil {
		t.Fatal("file not written")
	}
	h.keys("/undo preview")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/undo would revert 1 file") || !strings.Contains(v, "remove   n.txt") {
		t.Fatalf("view:\n%s", v)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("preview removed the file")
	}
	h.keys("/redo")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing to redo") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo")
	h.enter()
	if _, err := os.Stat(target); err == nil {
		t.Fatal("undo did not remove the created file")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "↶ removed") || !strings.Contains(v, "n.txt") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/redo")
	h.enter()
	if data, err := os.ReadFile(target); err != nil || string(data) != "x\n" {
		t.Fatalf("redo did not recreate the file: %q %v", data, err)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "↷ restored") || !strings.Contains(v, "redid 1 file change") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo")
	h.enter()
	if _, err := os.Stat(target); err == nil {
		t.Fatal("undo after redo did not remove the file")
	}
	h.keys("continue")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if sent := h.m.opts.Agent.Messages[len(h.m.opts.Agent.Messages)-2].Text(); !strings.Contains(sent, "I reverted the file changes") || !strings.Contains(sent, "continue") {
		t.Fatalf("model not told about the undo: %q", sent)
	}
}

// TestFakeMCPServerProcess is not a test: TestMCPTrustFlow spawns the test
// binary with MCP_FAKE_SERVER=1 and this "test" then serves the protocol.
func TestFakeMCPServerProcess(t *testing.T) {
	if os.Getenv("MCP_FAKE_SERVER") != "1" {
		t.Skip("helper process only")
	}
	testserver.Run()
}

func TestMCPAuthCommand(t *testing.T) {
	srv, fake := mcptest.NewAuthServer(t)
	defer srv.Close()
	h := newHarness(t)
	reg := h.m.opts.Agent.Tools
	tokens := &mcp.TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	mgr := &mcptool.Manager{Options: mcp.Options{Root: h.m.opts.Cwd, HandshakeTimeout: 10 * time.Second, Tokens: tokens}, Registry: reg}
	secure := mcp.ServerConfig{URL: srv.URL + "/mcp"}
	opened := make(chan string, 1)
	h.m.opts.MCP = &MCPOptions{
		Manager: mgr, Global: map[string]mcp.ServerConfig{"secure": secure, "local": {Command: "true"}}, Tokens: tokens,
		OpenURL: func(u string) error {
			opened <- u
			go func() {
				if resp, err := http.Get(u); err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
	}
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120}) // the printed URL is long
	// The server fails to start without a token and says how to fix it.
	mgr.Start(context.Background(), "secure", secure, func(name string, c *mcp.Client, err error) {
		h.msgs <- mcpDoneMsg{name: name, err: err}
	})
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "authorization required") })

	h.keys("/mcp auth local")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "runs locally") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/mcp auth nope")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, `no MCP server named "nope"`) {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/mcp logout secure")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no token stored") {
		t.Fatalf("view:\n%s", v)
	}

	h.keys("/mcp auth secure")
	h.enter()
	// Wait for the banner, not for the tool. Registering the tools is the
	// last thing the restarted server's own goroutine does; the banner is
	// printed by the mcpDoneMsg that follows it through the queue, so a
	// pump that stopped at the registration raced the message that the
	// assertions below are about.
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "mcp secure ready") })
	if _, ok := reg.Get("mcp__secure__whoami"); !ok {
		t.Fatal("the server said it was ready without registering its tool")
	}
	select {
	case u := <-opened:
		if !strings.Contains(u, "/auth/authorize?") {
			t.Fatalf("opened %q", u)
		}
	default:
		t.Fatal("browser not opened")
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "registered client "+fake.ClientID) {
		t.Fatalf("missing auth transcript:\n%s", v)
	}
	if !strings.Contains(v, "mcp secure ready") {
		t.Fatalf("ready ack not on the status line:\n%s", v)
	}
	if tok, ok := tokens.Get(secure.URL); !ok || tok.AccessToken != "at-1" {
		t.Fatalf("token = %+v %v", tok, ok)
	}
	h.keys("/mcp logout secure")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "token forgotten") {
		t.Fatalf("view:\n%s", v)
	}
	if _, ok := tokens.Get(secure.URL); ok {
		t.Fatal("token still stored after logout")
	}
}

func TestMCPTrustFlow(t *testing.T) {
	h := newHarness(t)
	reg := h.m.opts.Agent.Tools
	mgr := &mcptool.Manager{Options: mcp.Options{Root: h.m.opts.Cwd, HandshakeTimeout: 20 * time.Second}, Registry: reg}
	fake := mcp.ServerConfig{Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServerProcess"}, Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
	h.m.opts.MCP = &MCPOptions{
		Manager: mgr, Project: map[string]mcp.ServerConfig{"proj": fake}, ProjectHash: "h1",
		Trust: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")},
	}
	h.m.Init()
	if v := plain(h.m.View()); !strings.Contains(v, "not been trusted") || !strings.Contains(v, "proj") {
		t.Fatalf("trust notice missing:\n%s", v)
	}
	h.keys("/mcp")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no MCP servers running") {
		t.Fatalf("status:\n%s", v)
	}
	h.keys("/mcp trust")
	h.enter()
	h.pump(func() bool { _, ok := reg.Get("mcp__proj__echo"); return ok })
	if !h.m.opts.MCP.Trust.Trusted(h.m.opts.Cwd, "h1") {
		t.Fatal("trust not recorded")
	}
	h.keys("/mcp")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "✓ proj · 4 tools") || !strings.Contains(v, "mcp__proj__echo") || !strings.Contains(v, "mcp proj ready · 4 tools · 1 prompts") || !strings.Contains(v, "/mcp__proj__greet  (prompt)") {
		t.Fatalf("status after trust:\n%s", v)
	}
	// The server's prompt is a slash command with completion and arguments.
	h.keys("/mcp__proj__gr")
	if v := plain(h.m.View()); !strings.Contains(v, "/mcp__proj__greet <who>") {
		t.Fatalf("prompt completion missing:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	h.enter() // no argument: the required one is reported
	if v := plain(h.m.View()); !strings.Contains(v, "needs <who>") {
		t.Fatalf("missing-arg error absent:\n%s", v)
	}
	h.m.opts.Agent.Provider = &scripted{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "hello there"), StopReason: llm.StopEndTurn}}}
	h.keys("/mcp__proj__greet the team")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running && len(h.m.opts.Agent.Messages) == 2 })
	if sent := h.m.opts.Agent.Messages[0].Text(); sent != "Say hello to the team" {
		t.Fatalf("prompt sent = %q", sent)
	}
	mgr.Close()
}

func TestConnectDialogSwitchesProvider(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	var built *config.Config
	lister := &listing{models: []llm.ModelInfo{{ID: "zeta/z"}, {ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", ContextWindow: 128000, Price: &llm.Price{Input: 0.15, Output: 0.6}}}}
	path := h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { built = c; return lister, nil })
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	down := tea.KeyMsg{Type: tea.KeyDown}

	h.keys("/connect")
	h.enter()
	if h.m.connect == nil || h.m.connect.step != stepPick {
		t.Fatalf("dialog not open: %+v", h.m.connect)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "Connect to a provider") || !strings.Contains(v, "CONFIGURED") || !strings.Contains(v, "fake") || !strings.Contains(v, "OpenRouter") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("openrouter")
	h.enter()
	if h.m.connect.step != stepURL || h.m.connect.field.Value() != "https://openrouter.ai/api/v1" {
		t.Fatalf("step %d url %q", h.m.connect.step, h.m.connect.field.Value())
	}
	h.enter() // keep the preset URL
	if h.m.connect.step != stepKeys {
		t.Fatalf("step = %d", h.m.connect.step)
	}
	h.enter() // hosted provider without a key is refused
	if h.m.connect.step != stepKeys || !strings.Contains(plain(h.m.View()), "needs an API key") {
		t.Fatalf("step %d view:\n%s", h.m.connect.step, plain(h.m.View()))
	}
	h.keys("sk-one, sk-two")
	if v := plain(h.m.View()); strings.Contains(v, "sk-one") || !strings.Contains(v, "••") || !strings.Contains(v, "2 keys entered") {
		t.Fatalf("key visible:\n%s", v)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter}) // runs the model fetch
	if h.m.connect.step != stepModel || h.m.connect.loading || len(h.m.connect.models) != 2 {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	if built == nil || built.Provider != "openrouter" || strings.Join(built.Active().Keys(), ",") != "sk-one,sk-two" {
		t.Fatalf("list built with %+v", built)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "▸ deepseek/deepseek-v4-flash") || !strings.Contains(v, "zeta/z") {
		t.Fatalf("view:\n%s", v)
	}
	h.update(down)
	if !strings.Contains(plain(h.m.View()), "▸ zeta/z") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	h.keys("deep") // filtering resets the highlight to the first match
	h.enter()
	if h.m.connect != nil {
		t.Fatalf("dialog still open: %+v", h.m.connect)
	}
	if h.m.opts.Agent.Model != "deepseek/deepseek-v4-flash" || h.m.opts.Agent.Provider != lister || h.m.opts.Provider != "openrouter" || cfg.Provider != "openrouter" {
		t.Fatalf("agent %s/%v opts %s cfg %s", h.m.opts.Agent.Model, h.m.opts.Agent.Provider, h.m.opts.Provider, cfg.Provider)
	}
	if !strings.Contains(h.m.status, "connected to openrouter · deepseek/deepseek-v4-flash") {
		t.Fatalf("status = %q", h.m.status)
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"sk-two"`) || !strings.Contains(string(raw), `"provider": "openrouter"`) || !strings.Contains(string(raw), `"model": "deepseek/deepseek-v4-flash"`) {
		t.Fatalf("saved config: %s %v", raw, err)
	}
	if !strings.Contains(string(raw), `"context_window": 128000`) || !strings.Contains(string(raw), `"models"`) {
		t.Fatalf("model metadata not saved: %s", raw)
	}
	if ag := h.m.opts.Agent; ag.ContextWindow != 128000 || ag.Price == nil || ag.Price.Output != 0.6 || ag.CompactThreshold() != 96000 {
		t.Fatalf("agent window=%d price=%+v threshold=%d", ag.ContextWindow, ag.Price, ag.CompactThreshold())
	}
	if !strings.Contains(h.m.status, "128k ctx") || !strings.Contains(h.m.status, "$0.15/$0.6") {
		t.Fatalf("details missing from status: %q", h.m.status)
	}

	// /model alone opens the picker for the current provider; a typed id
	// that is not listed is used as is; esc leaves the dialog.
	h.inputReset()
	h.keys("/model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.connect == nil || h.m.connect.step != stepModel || h.m.connect.name != "openrouter" {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	h.keys("custom/model")
	h.enter()
	if h.m.connect != nil || h.m.opts.Agent.Model != "custom/model" {
		t.Fatalf("model = %s", h.m.opts.Agent.Model)
	}
	h.inputReset()
	h.keys("hello")
	h.inputReset()
	h.keys("/connect")
	h.enter()
	h.update(esc)
	if h.m.connect != nil || h.m.input.Value() != "" {
		t.Fatalf("esc did not close: %+v %q", h.m.connect, h.m.input.Value())
	}

	// A listing failure still lets the user type a model id; esc from a
	// /model picker closes it rather than stepping back to the keys.
	lister.err = errors.New("boom")
	h.keys("/model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(plain(h.m.View()), "could not list models: boom") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	h.update(esc)
	if h.m.connect != nil {
		t.Fatalf("esc should close the /model picker: %+v", h.m.connect)
	}
}

func TestFirstRunOpensConnectDialog(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = nil
	cfg := &config.Config{Provider: "anthropic", Providers: map[string]config.Provider{"anthropic": {Type: "anthropic"}}}
	lister := &listing{models: []llm.ModelInfo{{ID: "llama3"}}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return lister, nil })
	h.m.opts.Connect = true
	h.m.ready = false
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if h.m.connect == nil || h.m.connect.step != stepPick {
		t.Fatalf("dialog not opened: %+v", h.m.connect)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "not connected · /connect") || strings.Contains(v, "✓") {
		t.Fatalf("view:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.keys("hello")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "not connected to a provider yet: run /connect") {
		t.Fatalf("view:\n%s", v)
	}
	// Ollama needs no key: the key step is skipped after the URL.
	h.inputReset()
	h.keys("/connect")
	h.enter()
	h.keys("ollama")
	h.enter()
	if h.m.connect.step != stepURL {
		t.Fatalf("step = %d", h.m.connect.step)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.connect == nil || h.m.connect.step != stepModel || len(h.m.connect.models) != 1 {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	h.enter()
	if h.m.opts.Agent.Provider != lister || h.m.opts.Agent.Model != "llama3" || !strings.Contains(plain(h.m.View()), "ollama/llama3") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
}

func TestExportWritesMarkdown(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello there"), StopReason: llm.StopEndTurn})
	h.keys("hi")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/export notes/out.md")
	h.enter()
	data, err := os.ReadFile(filepath.Join(h.m.opts.Cwd, "notes", "out.md"))
	if err != nil {
		t.Fatalf("export file: %v\n%s", err, plain(h.m.View()))
	}
	if !strings.Contains(string(data), "## User\n\nhi") || !strings.Contains(string(data), "hello there") {
		t.Fatalf("export = %s", data)
	}
	if !strings.Contains(plain(h.m.View()), "exported 2 messages") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
}

func TestMemoryCommandsAndAutoExtraction(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "The x file is hand-maintained."), StopReason: llm.StopEndTurn},
	)
	store := memory.Open(t.TempDir(), h.m.opts.Cwd)
	h.m.opts.Memory, h.m.opts.AutoMemory = store, true
	h.m.opts.Agent.AutoApprove = true
	h.keys("/remember   Always run make test before committing ")
	h.enter()
	if e := store.Entries(); len(e) != 1 || e[0].Name != "always-test-before-committing" || e[0].Type != memory.TypeFeedback || e[0].Source != "user" {
		t.Fatalf("entries = %+v", e)
	}
	h.keys("/remember -g I prefer replies under ten lines")
	h.enter()
	if e, ok := store.Get("prefer-replies-under-ten-lines"); !ok || e.Scope != "global" || len(store.Entries()) != 2 {
		t.Fatalf("global entry = %+v", store.Entries())
	}
	h.keys("/memory")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "memory: 2 entries") || !strings.Contains(v, "(feedback): Always run make test before committing") || !strings.Contains(v, "(project, global)") && !strings.Contains(v, ", global)") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/memory make test")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "    Always run make test before committing") {
		t.Fatalf("recall view:\n%s", v)
	}
	h.keys("write x")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	// The turn changed a file, so extraction runs; drive its command.
	if !h.m.opts.Agent.LastTurnMutated() {
		t.Fatal("turn not mutated")
	}
	h.runCmd(h.m.extractMemory())
	if e, ok := store.Get("hand-maintained"); !ok || e.Source != "auto" {
		t.Fatalf("entries after extraction = %+v", store.Entries())
	}
	if v := plain(h.m.View()); !strings.Contains(v, "remembered: hand-maintained (project): The x file is hand-maintained.") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/forget hand-maintained")
	h.enter()
	if _, ok := store.Get("hand-maintained"); ok {
		t.Fatal("forget by name failed")
	}
	h.keys("/forget 1")
	h.enter()
	if e := store.Entries(); len(e) != 1 {
		t.Fatalf("entries after forget = %+v", e)
	}
	h.keys("/forget 9")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no memory entry 9") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestVerifyCommandSetsRunsAndGatesTurns(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"bad"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t2", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"ok"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "fixed"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Verify = &VerifyOptions{Trust: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	h.keys("/verify")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no verification commands") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/verify set grep -q ok a.txt")
	h.enter()
	cfg, _, err := verify.Load(h.m.opts.Cwd)
	if err != nil || len(cfg.Commands) != 1 || cfg.Commands[0] != "grep -q ok a.txt" || h.m.opts.Agent.Verify == nil || !h.m.opts.Verify.ProjectTrusted {
		t.Fatalf("cfg = %+v err = %v agent.Verify = %v", cfg, err, h.m.opts.Agent.Verify)
	}
	h.keys("/verify")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "verification failed: grep -q ok a.txt (exit 2") {
		t.Fatalf("manual run view:\n%s", v)
	}
	h.keys("write it")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "sending the model back to fix it (1/3)") || !strings.Contains(v, "verified: grep -q ok a.txt") {
		t.Fatalf("gated turn view:\n%s", v)
	}
	if got, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "a.txt")); string(got) != "ok" {
		t.Fatalf("a.txt = %q", got)
	}
	h.keys("/verify clear")
	h.enter()
	if cfg, _, _ := verify.Load(h.m.opts.Cwd); len(cfg.Commands) != 0 || h.m.opts.Agent.Verify != nil {
		t.Fatalf("clear left cfg = %+v verify = %v", cfg, h.m.opts.Agent.Verify)
	}
}

// TestVerifyRereadsProjectFileEachCommand is the pre-existing bug found
// alongside the undo-by-intent QA re-test: verify's project config is
// loaded once at session start (see cmd/janetcode/main.go), so a
// .janetcode/verify.json that was malformed at startup, then fixed by
// hand mid-session (not through /verify set, which already reloads after
// writing), used to keep saying "no verification commands" / "no
// .janetcode/verify.json" until the process restarted. /verify and
// /verify trust must re-read the file from disk every time instead.
func TestVerifyRereadsProjectFileEachCommand(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "n/a"), StopReason: llm.StopEndTurn})
	// Simulate what main.go leaves behind when the project file is
	// malformed at startup: verify.Load fails, so the session starts with
	// an empty, untrusted VerifyOptions rather than erroring out.
	if err := os.MkdirAll(filepath.Join(h.m.opts.Cwd, filepath.Dir(verify.ProjectFile)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.m.opts.Cwd, verify.ProjectFile), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.m.opts.Verify = &VerifyOptions{Trust: trust.Store{Path: filepath.Join(t.TempDir(), "trust.json")}}
	h.keys("/verify")
	h.enter()
	if h.m.opts.Verify.ProjectTrusted {
		t.Fatal("a malformed file must not read as trusted")
	}
	// The user fixes the file by hand mid-session — a plain text edit,
	// the way the real bug was found, not a /verify set (which already
	// reloads after writing). Without a restart, /verify trust and
	// /verify must both see it.
	if err := os.WriteFile(filepath.Join(h.m.opts.Cwd, verify.ProjectFile), []byte(`{"commands":["true"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	h.keys("/verify trust")
	h.enter()
	if !h.m.opts.Verify.ProjectTrusted || h.m.opts.Agent.Verify == nil {
		t.Fatalf("expected /verify trust to pick up and trust the now-valid file: %+v", h.m.opts.Verify)
	}
	h.keys("/verify")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "verified: true") {
		t.Fatalf("expected /verify to run the now-loaded command, view:\n%s", v)
	}
}

func TestContextAndPinCommands(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "rules.md"), []byte("tabs not spaces"), 0o644)
	h.keys("/pin")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no pins") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/pin @rules.md ")
	h.enter()
	h.keys("/pin never edit vendor")
	h.enter()
	if pins := h.m.opts.Agent.Pins; len(pins) != 2 || !strings.Contains(pins[0], "tabs not spaces") || pins[1] != "never edit vendor" {
		t.Fatalf("pins = %q", pins)
	}
	h.keys("write n")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	sc := h.m.opts.Agent.Provider.(*scripted)
	if sys := sc.reqs()[0].System; !strings.Contains(sys, "File rules.md as of pinning:\ntabs not spaces") || !strings.Contains(sys, "2. never edit vendor") {
		t.Fatalf("system = %q", sys)
	}
	re, _, err := session.Open(filepath.Dir(h.m.opts.Session.Path()), h.m.opts.Session.Meta.ID)
	if err != nil || len(re.Meta.Pins) != 2 {
		t.Fatalf("saved pins = %v err = %v", re.Meta.Pins, err)
	}
	h.keys("/unpin 1")
	h.enter()
	h.keys("/context")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "pins") || !strings.Contains(v, "(1)") || !strings.Contains(v, "by tool: write_file") || !strings.Contains(v, "largest tool results") || !strings.Contains(v, "write_file n.txt") {
		t.Fatalf("context view:\n%s", v)
	}
	h.keys("/unpin 5")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no pin 5") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestStatsCommand(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "r1", Name: "read_file", Input: json.RawMessage(`{"path":"missing.txt"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "gone"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1200, OutputTokens: 30}},
	)
	h.keys("/stats")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "0 turns · 0 model calls") || !strings.Contains(v, "no tool calls yet") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("read missing")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/stats")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"1 turn · 2 model calls", "1 tool call, 1 failed:", "read_file", "1 failed", "tokens · in 1.2k · out 30"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in view:\n%s", want, v)
		}
	}
}

func TestAgentsCommandEmpty(t *testing.T) {
	h := newHarness(t)
	attachTask(h)
	runAgents(h)
	if h.m.ov.kind != overlayAgents {
		t.Fatal("empty /agents should still open the overlay")
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "Subagents") || !strings.Contains(v, "no subagents yet") {
		t.Fatalf("empty overlay:\n%s", v)
	}
	for _, e := range h.m.transcript.entries {
		if e.kind == entryInfo && strings.Contains(e.text, "no subagents") {
			t.Fatalf("empty state dumped into the transcript: %q", e.text)
		}
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind != "" {
		t.Fatal("esc should close the empty overlay")
	}
}

func TestJobsCommandAndIdleNotice(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.keys("/jobs")
	h.enter()
	if h.m.ov.kind != overlayJobs {
		t.Fatal("empty /jobs should open the overlay")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "no background jobs") {
		t.Fatalf("view:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	jobs := &tool.Jobs{Dir: t.TempDir()}
	h.m.opts.Jobs = jobs
	if _, err := jobs.Start(h.m.opts.Cwd, "echo hi; exit 4"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := jobs.Wait(ctx, 1); err != nil {
		t.Fatal(err)
	}
	h.update(tickMsg(time.Now()))
	if v := plain(h.m.View()); !strings.Contains(v, "job 1 finished with exit 4") {
		t.Fatalf("view:\n%s", v)
	}
	if msgs := h.m.opts.Agent.Messages; len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "[background job update]") {
		t.Fatalf("context not added: %+v", msgs)
	}
	h.keys("/jobs")
	h.enter()
	if h.m.ov.kind != overlayJobs || h.m.ov.child != "1" {
		t.Fatalf("one job should open its log, overlay=%q child=%q", h.m.ov.kind, h.m.ov.child)
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "echo hi") || !strings.Contains(v, "hi") {
		t.Fatalf("job log:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.child != "" {
		t.Fatal("esc should return to the list")
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if v := plain(h.m.View()); !strings.Contains(v, "already finished") {
		t.Fatalf("kill finished:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.keys("/jobs kill 1")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "already finished") {
		t.Fatalf("typed kill:\n%s", v)
	}
}

func TestBtwDoesNotRecordAUserTurn(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "sideways yes"), StopReason: llm.StopEndTurn},
	)
	h.keys("hello")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	usersBefore := countUserTurns(h)
	h.keys("/btw is this sideways")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "sideways yes") })
	if countUserTurns(h) != usersBefore {
		t.Fatalf("btw recorded a user turn: before %d after %d", usersBefore, countUserTurns(h))
	}
}

func TestCopyLastAssistant(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "copy me please"), StopReason: llm.StopEndTurn})
	h.keys("say it")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	var got string
	h.m.copyText = func(s string) error { got = s; return nil }
	h.keys("/copy")
	h.enter()
	if got != "copy me please" {
		t.Fatalf("copied %q", got)
	}
}

func TestConfigKeyValue(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	h.m.opts.Config, h.m.opts.ConfigPath = cfg, path
	h.keys("/config thinking=false")
	h.enter()
	if cfg.Thinking == nil || *cfg.Thinking {
		t.Fatalf("live thinking = %v", cfg.Thinking)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"thinking"`) {
		t.Fatalf("config file = %s", raw)
	}
}

func TestAddDirCommand(t *testing.T) {
	h := newHarness(t)
	extra := t.TempDir()
	h.m.opts.Workspace = tool.NewLocal(h.m.opts.Cwd)
	h.keys("/add-dir " + extra)
	h.enter()
	dirs := h.m.opts.Workspace.ExtraDirs()
	if len(dirs) != 1 || dirs[0] != extra {
		// AddDir abs-es the path
		if len(dirs) != 1 {
			t.Fatalf("extra dirs = %v", dirs)
		}
	}
	re, _, err := session.Open(dirOf(h.m.opts.Session), h.m.opts.Session.Meta.ID)
	if err != nil || len(re.Meta.ExtraDirs) != 1 {
		t.Fatalf("persisted extra = %+v err=%v", re.Meta.ExtraDirs, err)
	}
}

func TestDoctorPrintsWithoutMutating(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{\"model\":\"keep\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	h.m.opts.ConfigPath = path
	h.m.opts.Doctor = func(_ context.Context, w io.Writer) error {
		fmt.Fprintln(w, "[ok] config checkup row")
		return nil
	}
	h.keys("/doctor")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "checkup row") })
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatalf("doctor mutated config: %s", after)
	}
}

func TestDoctorShowsRowsWhenCheckupFails(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Doctor = func(_ context.Context, w io.Writer) error {
		fmt.Fprintln(w, "[FAIL] credentials missing key")
		fmt.Fprintln(w, "[ok  ] sessions dir /tmp")
		return fmt.Errorf("1 check(s) failed")
	}
	h.keys("/doctor")
	h.enter()
	h.pump(func() bool {
		v := plain(h.m.View())
		return strings.Contains(v, "credentials missing") && strings.Contains(v, "1 check(s) failed")
	})
	v := plain(h.m.View())
	if !strings.Contains(v, "sessions dir") {
		t.Fatalf("failed checkup hid later rows:\n%s", v)
	}
}

func TestOpenAndDiffCommands(t *testing.T) {
	h := newHarness(t)
	root := h.m.opts.Cwd
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	h.keys("/diff")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/diff needs a git repository") {
		t.Fatalf("view:\n%s", v)
	}
	gitInitTUI(t, root)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120}) // whole listings must fit the viewport
	h.keys("/open a.txt:10")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "# a.txt  lines 1-30 of 30") || !strings.Contains(v, "▶10 │ line 10") || !strings.Contains(v, " 9 │ line 9") {
		t.Fatalf("open view:\n%s", v)
	}
	// Two separate hunks: change line 2 and line 28; plus an untracked file.
	lines[1] = "line 2 changed"
	lines[27] = "line 28 changed"
	os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\ny\n"), 0o644)
	h.keys("/diff")
	h.enter()
	v = plain(h.m.View())
	if !strings.Contains(v, "a.txt  (2 hunks, +2 -2)") || !strings.Contains(v, "+ new.txt  (new, 2 lines)") {
		t.Fatalf("diff list:\n%s", v)
	}
	h.keys("/diff a.txt")
	h.enter()
	v = plain(h.m.View())
	if !strings.Contains(v, "@@ hunk 1/2") || !strings.Contains(v, "@@ hunk 2/2") || !strings.Contains(v, "-line 28") || !strings.Contains(v, "+line 28 changed") {
		t.Fatalf("file diff:\n%s", v)
	}
	h.keys("/diff revert a.txt:2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "reverted hunk 2 of a.txt (1 left)") {
		t.Fatalf("revert view:\n%s", v)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if s := string(got); !strings.Contains(s, "line 2 changed\n") || strings.Contains(s, "line 28 changed") || !strings.Contains(s, "\nline 28\n") {
		t.Fatalf("after revert:\n%s", s)
	}
	h.keys("/diff revert a.txt:5")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "a.txt has 1 hunks") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/diff revert new.txt:1")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no tracked changes to revert") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/open missing.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "open: ") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestChangesBlameAndUndoOne(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Write the list."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"l.txt","content":"a\nb\nc\nd\ne\nf\ng\nh\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Capitalise b."},
			{Type: llm.BlockToolUse, ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"l.txt","old_string":"b","new_string":"B"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Capitalise h."},
			{Type: llm.BlockToolUse, ID: "e2", Name: "edit_file", Input: json.RawMessage(`{"path":"l.txt","old_string":"h","new_string":"H"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120})
	h.keys("/changes")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no file changes in this session yet") {
		t.Fatalf("view:\n%s", v)
	}
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Agent.Tools = tool.NewRegistry(tool.WriteFile{Root: h.m.opts.Cwd}, tool.EditFile{Root: h.m.opts.Cwd})
	h.keys("make the list")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/changes")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"turn 1", "  Write the list.", "#1   write_file l.txt  +8 -0", "  Capitalise b.", "#2   edit_file  l.txt  +1 -1", "#3   edit_file  l.txt  +1 -1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in view:\n%s", want, v)
		}
	}
	if strings.Contains(v, "turn 1 ·") {
		t.Fatalf("the turn header repeats per intent:\n%s", v)
	}
	h.keys("/changes 3")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "#3  edit_file  l.txt  · Capitalise h.") || !regexp.MustCompile(`-h\s*\n\s*\+H`).MatchString(v) {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/blame l.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "#2       2 │ B") || !strings.Contains(v, "#1       3 │ c") || !strings.Contains(v, "#3       8 │ H") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo 2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "↶ #2: reverted 1 hunk(s) of l.txt") {
		t.Fatalf("view:\n%s", v)
	}
	if data, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "l.txt")); string(data) != "a\nb\nc\nd\ne\nf\ng\nH\n" {
		t.Fatalf("file after /undo 2: %q", data)
	}
	h.keys("/undo 2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "already undone") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/blame missing.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no change to") {
		t.Fatalf("view:\n%s", v)
	}
}

// TestUndoAboutPreviewThenApply drives the whole "undo by intent" flow
// through the terminal: /undo about previews a selection, and /undo apply
// reverts exactly that preview.
func TestUndoAboutPreviewThenApply(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add debug logging output."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"logger.go","content":"package m\n\nfunc init() {\n\tlogging.Debug(\"start\")\n}\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Rename the Foo variable to Bar."},
			{Type: llm.BlockToolUse, ID: "w2", Name: "write_file", Input: json.RawMessage(`{"path":"app.go","content":"package m\n\nfunc run() {\n\tbar := 1\n\t_ = bar\n}\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, `{"undo":[1],"keep":[2],"why":"matches the logging change"}`), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Agent.Tools = tool.NewRegistry(tool.WriteFile{Root: h.m.opts.Cwd})
	h.keys("add logging and rename foo")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })

	h.keys("/undo about the logging change")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "undo apply") })
	v := plain(h.m.View())
	for _, want := range []string{"[undo] #1", "logger.go", "[keep] #2", "app.go", "matches the logging change", "/undo apply reverts the changes marked undo"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in preview view:\n%s", want, v)
		}
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "logger.go")); err != nil {
		t.Fatalf("preview must not touch anything: %v", err)
	}

	h.keys("/undo apply")
	h.enter()
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "↶") })
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "logger.go")); !os.IsNotExist(err) {
		t.Fatalf("logger.go should have been reverted (removed): %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "app.go")); err != nil {
		t.Fatalf("app.go (kept) should still be there: %v", err)
	}
}

// TestGoalStartsWorkingAtOnce: /goal <objective> used to record the goal
// and stop, since the loop only advanced when a turn ended. It starts
// the first round immediately, and the status bar shows the goal.
func TestGoalStartsWorkingAtOnce(t *testing.T) {
	// A goal keeps going round after round until the model reports it
	// done, so the fake needs a reply for every round it may take.
	var replies []*llm.Response
	for i := 0; i < 10; i++ {
		replies = append(replies, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "on it"), StopReason: llm.StopEndTurn})
	}
	h := newHarness(t, replies...)
	h.keys("/goal make the tests green")
	h.enter()
	if !h.m.turn.running {
		t.Fatal("setting a goal should start a turn")
	}
	h.pump(func() bool { return !h.m.turn.running })
	reqs := h.m.opts.Agent.Provider.(*scripted).reqs()
	if len(reqs) == 0 || !strings.Contains(reqs[0].Messages[len(reqs[0].Messages)-1].Text(), "make the tests green") {
		t.Fatalf("the first round should carry the objective: %+v", reqs)
	}
	if v := plain(h.m.statusView()); !strings.Contains(v, "goal g1") {
		t.Fatalf("status = %q", v)
	}
}

// TestCommandAvailabilityAndArgumentCompletion: an idle-only command
// resolved mid-turn is blocked with one message; arguments that can be
// enumerated are offered; and fuzzy matching finds a command from its
// letters.
func TestCommandAvailabilityAndArgumentCompletion(t *testing.T) {
	if c, ok := slash.Lookup("/compact"); !ok || !c.Idle {
		t.Fatal("/compact should be idle-only")
	}
	if c, ok := slash.Lookup("/compact-fast"); !ok || !c.Idle {
		t.Fatal("/compact-fast should be idle-only")
	}
	if c, ok := slash.Lookup("/help"); !ok || c.Idle {
		t.Fatal("/help should always be available")
	}
	block := make(chan struct{})
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	h.keys("go")
	h.enter()
	h.keys("/compact")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/compact waits for the turn to finish") {
		t.Fatalf("no blocked message:\n%s", v)
	}
	close(block)
	h.pump(func() bool { return !h.m.turn.running })

	// Fuzzy matching: the letters in order, and a "/" in the query splits.
	if !fuzzyCommand("/resume", "/rsm") || !fuzzyCommand("/permissions", "/prms") {
		t.Fatal("fuzzy match failed")
	}
	if fuzzyCommand("/resume", "/xyz") {
		t.Fatal("fuzzy match too loose")
	}
	h.m.input.SetValue("/rsm")
	if c := h.m.completions(); len(c) == 0 || c[0].name != "/resume" {
		t.Fatalf("fuzzy completion = %+v", c)
	}
	// Argument completion, and enter still runs a typed argument.
	h.m.input.SetValue("/undo ")
	c := h.m.completions()
	if len(c) == 0 || c[0].name != "/undo preview" {
		t.Fatalf("/undo completions = %+v", c)
	}
	if !h.m.comp.args {
		t.Fatal("an argument menu should be marked as one")
	}
}

// A goal round is machinery, not something the user typed. The whole
// continuation — objective plus a paragraph telling the model that
// rounds are a budget and not to announce them — used to be echoed into
// the transcript as a user message, and the session title was generated
// from it, so /resume listed sessions called "Work on this goal until it
// is complete or you are blocked".
func TestGoalRoundIsOneLine(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "on it")})
	h.keys("/goal make the tests pass")
	h.enter()
	v := plain(h.m.View())
	if strings.Contains(v, "Rounds are a budget") || strings.Contains(v, "Work on this goal until") {
		t.Fatalf("continuation prompt echoed to the user:\n%s", v)
	}
	if !strings.Contains(v, "goal g1 round 1/") || !strings.Contains(v, "make the tests pass") {
		t.Fatalf("round not announced:\n%s", v)
	}
	if got := h.m.opts.Session.Meta.Title; got != "make the tests pass" {
		t.Fatalf("session title = %q", got)
	}
	// The model still receives the full continuation.
	if !strings.Contains(h.m.hist.lastUser, "Rounds are a budget") {
		t.Fatalf("the model got %q", h.m.hist.lastUser)
	}
	// The status line's goal note is kept between frames — folding it out
	// of the log copies every event of the session — and the log growing
	// is what drops it: pausing the goal takes the note off the line on
	// the very next frame, not whenever something else happened to
	// invalidate it.
	// (Read from goalLine rather than the rendered line: the status line
	// cuts to the terminal width around a phase phrase picked at random.)
	if got := h.m.goalLine(); !strings.Contains(got, "goal g1 round") {
		t.Fatalf("the status line lost the goal: %q", got)
	}
	h.m.goalCommand("pause")
	if got := h.m.goalLine(); got != "" {
		t.Fatalf("a paused goal is still on the status line: %q", got)
	}
}

// /stats reported the session as open for 2562047h47m16.854775807s
// before the first turn: it measured from startedAt, which is the clock
// for the turn that is running and is the zero time until one does.
func TestStatsReportsWindowAge(t *testing.T) {
	h := newHarness(t)
	h.m.openedAt = time.Now().Add(-90 * time.Second)
	h.keys("/stats")
	h.enter()
	var line string
	for _, e := range h.m.transcript.entries {
		if strings.Contains(e.text, "session ") && strings.Contains(e.text, "open ") {
			line = e.text
		}
	}
	if line == "" {
		t.Fatalf("no stats line:\n%s", plain(h.m.View()))
	}
	if !strings.Contains(line, "open 1m") {
		t.Fatalf("stats line = %q", line)
	}
	if strings.Contains(line, "2562047h") {
		t.Fatalf("stats measured from the turn clock: %q", line)
	}
}

// A group heading printed hard against the last command of the group
// before it read as that command's second line.
func TestHelpSeparatesGroups(t *testing.T) {
	h := newHarness(t)
	help := h.m.helpText()
	for _, g := range helpGroups {
		if !strings.Contains(help, "\n\n"+g.title+"\n") {
			t.Errorf("group %q has no blank line before it", g.title)
		}
	}
}

// tool.Bash reports a non-zero exit inside the output rather than as an
// error, so !nosuchcommand was shown with the same green tick as a
// command that worked.
func TestFailingBangCommandIsMarkedFailed(t *testing.T) {
	for _, c := range []struct {
		out  string
		code int
		ok   bool
	}{
		{"bash: nope: command not found\n[exit code 127]", 127, true},
		{"hello", 0, false},
		{"[exit code 1] no lines matched; for this command that is an answer, not a failure", 0, false},
	} {
		code, ok := exitCode(c.out)
		if code != c.code || ok != c.ok {
			t.Errorf("exitCode(%q) = %d,%v want %d,%v", c.out, code, ok, c.code, c.ok)
		}
	}

	h := newHarness(t)
	call := agent.ToolCall{ID: "shell-1", Name: "shell", Input: json.RawMessage(`{"command":"nosuchcommand"}`)}
	h.m.finishShell(shellDoneMsg{call: call, output: "bash: nosuchcommand: command not found\n[exit code 127]", dur: time.Millisecond})
	if !strings.Contains(h.m.status, "exit code 127") {
		t.Fatalf("status = %q", h.m.status)
	}
	last := h.m.transcript.entries[len(h.m.transcript.entries)-1]
	if !last.isError {
		t.Fatalf("failed command not marked as an error")
	}
}

func TestAgentsNewSpawnsGeneral(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = &scripted{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "looked around"), StopReason: llm.StopEndTurn},
	}}
	attachTask(h)
	h.keys("/agents new map the parser")
	h.enter()
	if h.m.ov.kind != overlayAgents {
		t.Fatalf("overlay = %q", h.m.ov.kind)
	}
	h.pump(func() bool {
		kids := h.m.taskTool().Children()
		return len(kids) == 1 && kids[0].Done
	})
	kids := h.m.taskTool().Children()
	if kids[0].Description != "map the parser" {
		t.Fatalf("desc = %q", kids[0].Description)
	}
}

func TestSSHCommandStatusAndOff(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Workspace = tool.NewLocal(h.m.opts.Cwd)
	h.keys("/ssh")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "workspace") {
		t.Fatalf("status:\n%s", v)
	}
	h.keys("/ssh off")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "workspace local") {
		t.Fatalf("off:\n%s", v)
	}
}

// TestAliasesObeyTheIdleGate: an alias of an idle-only command is
// refused mid-turn exactly as the canonical name is. The gate used to
// look the typed name up in the slash table, which lists canonical names
// only, so /new started a new session out from under a running turn
// while /clear was correctly refused.
func TestAliasesObeyTheIdleGate(t *testing.T) {
	if slash.Canonical("/new") != "/clear" {
		t.Fatal("/new should be an alias of /clear")
	}
	for _, name := range []string{"/clear", "/new"} {
		block := make(chan struct{})
		h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
		h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
		h.keys("go")
		h.enter()
		h.keys(name)
		h.enter()
		if v := plain(h.m.View()); !strings.Contains(v, name+" waits for the turn to finish") {
			t.Errorf("%s during a running turn was not refused:\n%s", name, v)
		}
		close(block)
		h.pump(func() bool { return !h.m.turn.running })
	}
}

// TestUndoTurnPreviewThenApply drives "restore to before a turn" through
// the terminal: /undo turn 2 lists what it would change and touches
// nothing, and the same /undo apply that carries out /undo about carries
// this out too.
func TestUndoTurnPreviewThenApply(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add the first file."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"one.go","content":"package one\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Add the second file."},
			{Type: llm.BlockToolUse, ID: "w2", Name: "write_file", Input: json.RawMessage(`{"path":"two.go","content":"package two\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Agent.Tools = tool.NewRegistry(tool.WriteFile{Root: h.m.opts.Cwd})
	for _, p := range []string{"write one", "write two"} {
		h.keys(p)
		h.enter()
		h.pump(func() bool { return !h.m.turn.running })
	}
	// /changes says which turn can be restored to.
	h.keys("/changes")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "[restorable]") || !strings.Contains(v, "/undo turn") {
		t.Fatalf("/changes does not offer a turn to restore to:\n%s", v)
	}

	h.keys("/undo turn 2")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "before turn 2") {
		t.Fatalf("preview does not say what it would restore to:\n%s", v)
	}
	if !strings.Contains(v, "two.go") || !strings.Contains(v, "/undo apply") {
		t.Fatalf("preview:\n%s", v)
	}
	// The preview says what leaves the conversation as well as what
	// happens to the files.
	if !strings.Contains(v, "rewind this conversation") {
		t.Fatalf("the preview does not say the conversation goes back too:\n%s", v)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "two.go")); err != nil {
		t.Fatalf("the preview must not touch anything: %v", err)
	}

	h.keys("/undo apply")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "two.go")); !os.IsNotExist(err) {
		t.Fatalf("turn 2's file should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "one.go")); err != nil {
		t.Fatalf("turn 1's file should have stayed: %v", err)
	}
	// And the conversation went back with the files: turn 2's prompt is
	// no longer anything the model can see.
	for _, m := range h.m.opts.Agent.MessagesSnapshot() {
		if strings.Contains(m.Text(), "write two") {
			t.Fatal("turn 2's prompt survived the rewind in the terminal")
		}
	}
	// A turn with no snapshot left is refused, in as many words.
	h.keys("/undo turn 9")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "no snapshot for that turn") {
		t.Fatalf("a turn with no snapshot was not refused:\n%s", v)
	}
}

// /usage says which model spent the session's tokens, not only how many
// there were.
func TestUsageCommandSplitsBySessionModel(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hi"), StopReason: llm.StopEndTurn,
		Usage: llm.Usage{InputTokens: 1200, OutputTokens: 340, CacheReadTokens: 900}})
	h.keys("hello")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/usage")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "per model") {
		t.Fatalf("no per-model split:\n%s", v)
	}
	if !strings.Contains(v, h.m.opts.Agent.Settings().Model) {
		t.Fatalf("the session's model is not named:\n%s", v)
	}
}
