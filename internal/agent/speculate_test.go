package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// specScript plays both models. The cheap one, running the predicted
// prompt, writes b.txt, tries a command, and reports; the main one
// answers whatever it is sent, and records every request.
type specScript struct {
	mu   sync.Mutex
	main []llm.Request
}

func (s *specScript) Name() string { return "fake" }
func (s *specScript) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if req.Model == "cheap" {
		n := 0
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if b.Type == llm.BlockToolResult {
					n++
				}
			}
		}
		switch n {
		case 0:
			return toolCall("s1", "write_file", `{"path":"b.txt","content":"from the speculative run\n"}`), nil
		case 1:
			return toolCall("s2", "bash", `{"command":"echo outside > /tmp/specscript-should-not-run"}`), nil
		default:
			return text("SPECREPORT: wrote b.txt"), nil
		}
	}
	s.mu.Lock()
	s.main = append(s.main, req)
	s.mu.Unlock()
	return text("MAIN"), nil
}

func (s *specScript) lastMain() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.main) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range s.main[len(s.main)-1].Messages {
		b.WriteString(m.Text() + "\n")
	}
	return b.String()
}

// specAgent is a session in a git repository with one turn behind it,
// speculation on and a priced cheap slot.
func specAgent(t *testing.T) (*Agent, *specScript, string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	gitInit(t, root)
	prov := &specScript{}
	a := &Agent{Provider: prov, Model: "main", System: "sys", AutoApprove: true}
	a.Root = root
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root})
	a.RouteCheap = "cheap"
	a.Prices = func(string) *llm.Price { return &llm.Price{Input: 1, Output: 1} }
	a.Speculate = true
	if err := a.Run(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	return a, prov, root
}

func specWait(t *testing.T, a *Agent) *specRun {
	t.Helper()
	a.spec.mu.Lock()
	run := a.spec.run
	a.spec.mu.Unlock()
	if run == nil {
		t.Fatal("no speculative run")
	}
	<-run.done
	return run
}

func specNotices(a *Agent, prompt string) ([]string, error) {
	var notes []string
	err := a.Run(context.Background(), prompt, func(e Event) {
		if e.Kind == EvNotice {
			notes = append(notes, e.Text)
		}
	})
	return notes, err
}

func TestSpecAdoptsTheRunWhenThePredictedPromptIsSent(t *testing.T) {
	a, prov, root := specAgent(t)
	if ok, why := a.StartSpeculation(context.Background(), "add the b file"); !ok {
		t.Fatalf("not started: %s", why)
	}
	run := specWait(t, a)
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("the speculative run wrote to the real tree before it was adopted")
	}
	if _, err := os.Stat("/tmp/specscript-should-not-run"); err == nil {
		os.Remove("/tmp/specscript-should-not-run")
		t.Fatal("the speculative run ran a command the session never allowed")
	}
	before := a.Usage.OutputTokens
	// Case, spacing and a trailing full stop do not make it another prompt.
	notes, err := specNotices(a, "Add the  b file.")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "from the speculative run\n" {
		t.Fatalf("b.txt = %q, want the speculative run's", got)
	}
	sent := prov.lastMain()
	for _, want := range []string{"[speculative run adopted]", "b.txt", "SPECREPORT: wrote b.txt", "cheap"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("main model was not told %q:\n%s", want, sent)
		}
	}
	if !strings.Contains(strings.Join(notes, "\n"), "adopted the speculative run") {
		t.Fatalf("notices %q", notes)
	}
	found := false
	for _, c := range a.Changes() {
		if c.Tool == "speculate" && strings.HasSuffix(c.Path, "b.txt") {
			found = true
		}
	}
	if !found {
		t.Fatal("the adopted change is not on the undo record")
	}
	if a.Usage.OutputTokens <= before {
		t.Fatal("the speculative run's spend was not billed to the session")
	}
	if _, err := os.Stat(run.wt.Dir); err == nil {
		t.Fatal("checkout left behind after adoption")
	}
}

func TestSpecDiscardsOnAnyOtherPrompt(t *testing.T) {
	a, prov, root := specAgent(t)
	if ok, why := a.StartSpeculation(context.Background(), "add the b file"); !ok {
		t.Fatalf("not started: %s", why)
	}
	run := specWait(t, a)
	notes, err := specNotices(a, "add the b file and a c file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("a discarded run reached the real tree")
	}
	if strings.Contains(prov.lastMain(), "[speculative run adopted]") {
		t.Fatal("main model told of a discarded run")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "not the one predicted") {
		t.Fatalf("notices %q", notes)
	}
	if _, err := os.Stat(run.wt.Dir); err == nil {
		t.Fatal("checkout left behind after discard")
	}
	if out, _ := gitRun(context.Background(), root, "for-each-ref", "refs/janetcode/"); out != "" {
		t.Fatalf("snapshot ref left behind: %s", out)
	}
}

func TestSpecDiscardsWhenTheTreeMovedUnderIt(t *testing.T) {
	a, _, root := specAgent(t)
	a.StartSpeculation(context.Background(), "add the b file")
	specWait(t, a)
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("the user's own\n"), 0o644)
	notes, err := specNotices(a, "add the b file")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "the user's own\n" {
		t.Fatalf("b.txt overwritten: %q", got)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "changed in the project") {
		t.Fatalf("notices %q", notes)
	}
}

func TestSpecDiscardsWhenTheConversationMoved(t *testing.T) {
	a, _, root := specAgent(t)
	a.StartSpeculation(context.Background(), "add the b file")
	specWait(t, a)
	a.AddContext("I ran make by hand; it failed")
	notes, _ := specNotices(a, "add the b file")
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("adopted into a conversation it did not start from")
	}
	if !strings.Contains(strings.Join(notes, "\n"), "conversation changed") {
		t.Fatalf("notices %q", notes)
	}
}

func TestSpecAttachmentsAfterThePromptDoNotCount(t *testing.T) {
	a, _, root := specAgent(t)
	a.Prepare = func(_ context.Context, blocks []llm.Block) ([]llm.Block, string) {
		blocks[0].Text += "[repository map: 1 source files]\na.txt"
		return append(blocks, llm.Block{Type: llm.BlockText, Text: "<memory>use tabs</memory>"}), ""
	}
	a.StartSpeculation(context.Background(), "add the b file")
	specWait(t, a)
	if _, err := specNotices(a, "add the b file"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err != nil {
		t.Fatal("the repository map and memory attached to the prompt stopped the adoption")
	}
}

func TestSpecDiscardRemovesTheCheckout(t *testing.T) {
	a, _, _ := specAgent(t)
	a.StartSpeculation(context.Background(), "add the b file")
	run := specWait(t, a)
	a.DiscardSpeculation()
	if _, err := os.Stat(run.wt.Dir); err == nil {
		t.Fatal("checkout left behind")
	}
	a.DiscardSpeculation() // nothing left: a no-op
}

func TestSpecStartRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(a *Agent)
		want  string
	}{
		{"off", func(a *Agent) { a.Speculate = false }, "off"},
		{"no cheap slot", func(a *Agent) { a.RouteCheap = "" }, "cheap model"},
		{"unpriced", func(a *Agent) { a.Prices = nil }, "no price"},
		{"read-only", func(a *Agent) { a.UpdateSettings(func(s *Settings) { s.ReadOnly = true }) }, "read-only"},
		{"ceiling", func(a *Agent) { a.SetMaxCost(1e-6) }, "ceiling"},
		{"unclear", func(a *Agent) {}, "question"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, _ := specAgent(t)
			c.setup(a)
			prompt := "add the b file"
			if c.name == "unclear" {
				prompt = "should we add the b file?"
			}
			ok, why := a.StartSpeculation(context.Background(), prompt)
			if ok || !strings.Contains(why, c.want) {
				t.Fatalf("started=%v why=%q, want a refusal naming %q", ok, why, c.want)
			}
			if a.spec.run != nil {
				t.Fatal("a refused run was left registered")
			}
		})
	}
}

func TestSpecUnclear(t *testing.T) {
	for prompt, clear := range map[string]bool{
		"add a test for Parse":              true,
		"add error handling for the parser": true, // "or" inside a word
		"add a postfix parser":              true, // "post" inside a word
		"rename compute to calculate":       true,
		"commit the changes":                false,
		"push it":                           false,
		"run the tests or the linter":       false,
		"does it handle unicode?":           false,
		"ok":                                false,
		"remove the unused helper":          false, // conservative: deletions are not rehearsed
		"install the deps and build":        false,
	} {
		if got := specUnclear(prompt) == ""; got != clear {
			t.Errorf("%q: clear=%v, want %v (%s)", prompt, got, clear, specUnclear(prompt))
		}
	}
}

func TestSpecApproverLetsOnlyFileToolsThrough(t *testing.T) {
	for name, want := range map[string]Decision{"write_file": Allow, "edit_file": Allow, "bash": Deny, "web_fetch": Deny} {
		if d, _ := (specApprover{}).Approve(context.Background(), ToolCall{Name: name}); d != want {
			t.Errorf("%s: %v, want %v", name, d, want)
		}
	}
}
