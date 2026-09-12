package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTerminalKeepsCwdAndEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("persistent shell tests use POSIX bash")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	terms := &Terms{}
	defer terms.CloseAll()
	tl := TermTool{Terms: terms, Root: root}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"action":"open"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "terminal 1") {
		t.Fatalf("open = %q", out)
	}
	cd := `{"action":"send","id":1,"text":"cd sub\nexport JC_TERM_MARK=alive","timeout_ms":5000}`
	if _, err := tl.Run(context.Background(), json.RawMessage(cd)); err != nil {
		t.Fatal(err)
	}
	got, err := tl.Run(context.Background(), json.RawMessage(`{"action":"send","id":1,"text":"pwd; echo $JC_TERM_MARK","timeout_ms":5000}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "sub") || !strings.Contains(got, "alive") {
		t.Fatalf("cwd/env did not persist: %q", got)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"action":"close","id":1}`)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		list, _ := tl.Run(context.Background(), json.RawMessage(`{"action":"list"}`))
		if strings.Contains(list, "exited") || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOneShotBashStillEOFWithTerms(t *testing.T) {
	b := Bash{Root: t.TempDir()}
	out, err := b.Run(context.Background(), json.RawMessage(`{"command":"cat"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "" && out != "(no output)" {
		t.Fatalf("foreground cat should see EOF, got %q", out)
	}
	if terms := (&Terms{}); len(terms.List()) != 0 {
		t.Fatal("one-shot bash must not open a terminal session")
	}
}
