package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagesRoundTripAndDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p1, err := SavePage(path, " First ", []byte("<h1>one</h1>"))
	if err != nil || p1.N != 1 || p1.Title != "First" {
		t.Fatalf("SavePage 1 = %+v, %v", p1, err)
	}
	p2, err := SavePage(path, "Second", []byte("<h1>two</h1>"))
	if err != nil || p2.N != 2 {
		t.Fatalf("SavePage 2 = %+v, %v", p2, err)
	}
	got, html, err := LoadPage(path, 2)
	if err != nil || got.Title != "Second" || string(html) != "<h1>two</h1>" {
		t.Fatalf("LoadPage = %+v %q %v", got, html, err)
	}
	list, _ := ListPages(path)
	if len(list) != 2 || list[0].N != 1 || list[1].N != 2 {
		t.Fatalf("ListPages = %+v", list)
	}
	if _, err := os.Stat(PagePath(path, 1)); err != nil {
		t.Fatalf("page file should sit beside the session: %v", err)
	}
	if err := Delete(dir, "abc"); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "abc*"))
	if len(left) != 0 {
		t.Fatalf("Delete left %v", left)
	}
}

func TestSavePageLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if _, err := SavePage(path, "t", []byte("  \n")); err == nil {
		t.Error("blank page accepted")
	}
	big := []byte(strings.Repeat("x", MaxPageBytes+1))
	_, err := SavePage(path, "t", big)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversize err = %v, want a message naming the limit", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.page-*")); len(left) != 0 {
		t.Errorf("rejected page left files: %v", left)
	}
	if _, err := SavePage(path, "t", []byte(strings.Repeat("x", MaxPageBytes))); err != nil {
		t.Errorf("page at the limit refused: %v", err)
	}
	if _, err := SavePage("", "t", []byte("x")); err == nil {
		t.Error("empty session path accepted")
	}
}

func TestListPagesNone(t *testing.T) {
	p, err := ListPages(filepath.Join(t.TempDir(), "none.jsonl"))
	if err != nil || len(p) != 0 {
		t.Fatalf("ListPages = %v, %v", p, err)
	}
}
