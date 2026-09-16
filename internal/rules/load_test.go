package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAnOversizedRuleIsCutOnARuneBoundary(t *testing.T) {
	cwd := t.TempDir()
	// Multi-byte runes so that a byte-wise cut would land mid-rune.
	body := strings.Repeat("é", maxRuleBytes)
	write(t, filepath.Join(cwd, ".janetcode", "rules", "big.md"), "---\npaths: '*.go'\n---\n"+body+"\n")
	got := Load(cwd, "")
	if len(got) != 1 {
		t.Fatalf("rules = %+v", got)
	}
	if !utf8.ValidString(got[0].Body) {
		t.Fatal("truncation split a rune")
	}
	if len(got[0].Body) > maxRuleBytes+len("\n… (truncated)") {
		t.Fatalf("body is %d bytes, over the %d cap", len(got[0].Body), maxRuleBytes)
	}
	if !strings.HasSuffix(got[0].Body, "(truncated)") {
		t.Fatal("truncation is not announced")
	}
}

func TestARulesDirectoryIsBounded(t *testing.T) {
	cwd := t.TempDir()
	for i := 0; i < maxRules+20; i++ {
		write(t, filepath.Join(cwd, ".janetcode", "rules", fmt.Sprintf("r%03d.md", i)),
			"---\npaths: '*.go'\n---\nbody\n")
	}
	if n := len(Load(cwd, "")); n != maxRules {
		t.Fatalf("loaded %d rules, want the %d cap", n, maxRules)
	}
}

func TestLoadSkipsUnreadableAndEmptyFiles(t *testing.T) {
	cwd := t.TempDir()
	dir := filepath.Join(cwd, ".janetcode", "rules")
	write(t, filepath.Join(dir, "empty.md"), "---\npaths: '*.go'\n---\n   \n")
	write(t, filepath.Join(dir, "ok.md"), "---\npaths: '*.go'\n---\nkeep me\n")
	if err := os.Mkdir(filepath.Join(dir, "nested.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := Load(cwd, "")
	if len(got) != 1 || got[0].Name != "ok" {
		t.Fatalf("rules = %+v", got)
	}
}

func TestLoadNormalisesGlobsOnce(t *testing.T) {
	cwd := t.TempDir()
	write(t, filepath.Join(cwd, ".janetcode", "rules", "n.md"),
		"---\npaths: ' ./db/migrations/** ', './/x', ' '\n---\nbody\n")
	got := Load(cwd, "")
	if len(got) != 1 {
		t.Fatalf("rules = %+v", got)
	}
	for _, g := range got[0].Paths {
		if g != strings.TrimSpace(g) || strings.HasPrefix(g, "./") || g == "" {
			t.Fatalf("glob %q was not normalised: %+v", g, got[0].Paths)
		}
	}
	if !got[0].Matches("db/migrations/1.sql") {
		t.Fatalf("normalised globs stopped matching: %+v", got[0].Paths)
	}
}

func TestInjectorDoesNotHoldItsLockAcrossTheRulesCallback(t *testing.T) {
	inj := &Injector{}
	inj.Rules = func() []Rule {
		// Reading rules touches the disk; /rules must not be blocked
		// behind it, so Sent has to be callable from here.
		inj.Sent("anything")
		return []Rule{{Name: "sql", Paths: []string{"*.sql"}, Body: "b"}}
	}
	done := make(chan []string, 1)
	go func() { done <- inj.For([]string{"a.sql"}) }()
	select {
	case got := <-done:
		if len(got) != 1 {
			t.Fatalf("got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("For deadlocked against its own lock")
	}
}

func TestInjectorIsConcurrencySafe(t *testing.T) {
	rs := []Rule{{Name: "sql", Paths: []string{"*.sql"}, Body: "b"}}
	inj := &Injector{Rules: func() []Rule { return rs }}
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := inj.For([]string{"a.sql"})
			mu.Lock()
			total += len(got)
			mu.Unlock()
			inj.Sent("sql")
		}()
	}
	wg.Wait()
	if total != 1 {
		t.Fatalf("a rule went out %d times, want once", total)
	}
}
