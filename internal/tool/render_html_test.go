package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// png1 is a real 1x1 PNG.
var png1, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")

type savedPage struct {
	title string
	html  string
}

func newRender(t *testing.T) (RenderHTML, string, *[]savedPage) {
	t.Helper()
	root := t.TempDir()
	var saved []savedPage
	return RenderHTML{Root: root, Save: func(title string, html []byte) (int, string, error) {
		saved = append(saved, savedPage{title, string(html)})
		return len(saved), filepath.Join(root, "page.html"), nil
	}}, root, &saved
}

func renderInput(title, html string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"title": title, "html": html})
	return b
}

func TestRenderHTMLSavesAndReportsPath(t *testing.T) {
	r, root, saved := newRender(t)
	out, err := r.Run(context.Background(), renderInput("Q3 revenue", "<h1>hi</h1>"))
	if err != nil {
		t.Fatal(err)
	}
	if len(*saved) != 1 || (*saved)[0].title != "Q3 revenue" || (*saved)[0].html != "<h1>hi</h1>" {
		t.Fatalf("saved = %+v", *saved)
	}
	if n, ok := PageResultN(out); !ok || n != 1 {
		t.Errorf("PageResultN(%q) = %d, %v", out, n, ok)
	}
	line, ok := PageResultLine(out)
	if want := "▣ Q3 revenue · " + filepath.Join(root, "page.html"); !ok || line != want {
		t.Errorf("card = %q, want %q", line, want)
	}
	if got := PageResultTitle(out); got != "Q3 revenue" {
		t.Errorf("title = %q", got)
	}
}

func TestRenderHTMLRejectsBadInput(t *testing.T) {
	r, _, saved := newRender(t)
	big := strings.Repeat("x", MaxPageHTMLBytes+1)
	for name, in := range map[string]json.RawMessage{
		"no title": renderInput("  ", "<p>x</p>"),
		"no html":  renderInput("t", " \n"),
		"too big":  renderInput("t", big),
	} {
		if _, err := r.Run(context.Background(), in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err := r.Run(context.Background(), renderInput("t", big))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("size error = %v, want one naming the limit", err)
	}
	if len(*saved) != 0 {
		t.Errorf("rejected input was saved: %+v", *saved)
	}
}

func TestRenderHTMLEmbedsWorkspaceImages(t *testing.T) {
	r, root, saved := newRender(t)
	if err := os.MkdirAll(filepath.Join(root, "charts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "charts", "a.png"), png1, 0o644); err != nil {
		t.Fatal(err)
	}
	html := `<img src="charts/a.png"><img src='` + filepath.Join(root, "charts", "a.png") + `'>` +
		`<div style="background:url(charts/a.png)"></div><img src="https://cdn.test/x.png"><img src="data:image/png;base64,AAAA"><img src="${dyn}">`
	if _, err := r.Run(context.Background(), renderInput("t", html)); err != nil {
		t.Fatal(err)
	}
	got := (*saved)[0].html
	uri := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png1)
	if c := strings.Count(got, uri); c != 3 {
		t.Errorf("embedded %d times, want 3 in:\n%.300s", c, got)
	}
	for _, keep := range []string{`https://cdn.test/x.png`, `data:image/png;base64,AAAA`, `${dyn}`} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was rewritten", keep)
		}
	}
	if strings.Contains(got, "charts/a.png") {
		t.Error("a local path survived in the stored page")
	}
}

func TestRenderHTMLRefusesPathsOutsideWorkspace(t *testing.T) {
	r, root, saved := newRender(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "secret.png")
	if err := os.WriteFile(outside, png1, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.png")); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	for name, ref := range map[string]string{
		"absolute":  outside,
		"dotdot":    rel,
		"file url":  "file://" + outside,
		"symlink":   "link.png",
		"in css":    outside,
		"not image": "notes.txt",
	} {
		html := `<img src="` + ref + `">`
		if name == "in css" {
			html = `<style>a{background:url('` + ref + `')}</style>`
		}
		_, err := r.Run(context.Background(), renderInput("t", html))
		if err == nil {
			t.Errorf("%s: %q accepted", name, ref)
		}
		if len(*saved) != 0 {
			t.Errorf("%s: a refused page was saved: %+v", name, *saved)
		}
	}
}

func TestRenderHTMLImageSizeCaps(t *testing.T) {
	r, root, _ := newRender(t)
	if err := os.WriteFile(filepath.Join(root, "big.png"), make([]byte, maxPageImageBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := r.Run(context.Background(), renderInput("t", `<img src="big.png">`))
	if err == nil || !strings.Contains(err.Error(), "per-image limit") {
		t.Errorf("err = %v", err)
	}
}
