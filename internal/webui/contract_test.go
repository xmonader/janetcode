package webui

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Every /api/ path the page's scripts name has a route. A renamed or
// removed handler otherwise breaks a button with nothing but a 404 in the
// browser console to show for it.
func TestPageAPIPathsAreRouted(t *testing.T) {
	mux := newServer(context.Background(), Options{}).routes()
	apiPath := regexp.MustCompile(`/api/[a-z_]+(?:/[a-z_]+)*`)
	seen := 0
	err := fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") || strings.Contains(path, "/vendor/") {
			return err
		}
		src, err := fs.ReadFile(staticFS, path)
		if err != nil {
			return err
		}
		for _, p := range apiPath.FindAllString(string(src), -1) {
			seen++
			if _, pattern := mux.Handler(httptest.NewRequest("GET", p, nil)); pattern == "/" || pattern == "" {
				t.Errorf("%s calls %s, which no route serves", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen < 20 {
		t.Fatalf("found only %d /api/ paths in the scripts; the pattern no longer matches how they are written", seen)
	}
}

// Every script, stylesheet and font index.html links to is served with a
// body: an asset renamed without its link leaves the page unstyled or
// dead.
func TestPageAssetsServe(t *testing.T) {
	h := newServer(context.Background(), Options{Addr: "127.0.0.1:8787"}).handler()
	index, err := fs.ReadFile(staticFS, "static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	refs := regexp.MustCompile(`(?:src|href)="(/static/[^"?#]+)`).FindAllStringSubmatch(string(index), -1)
	if len(refs) < 5 {
		t.Fatalf("found only %d asset links in index.html", len(refs))
	}
	for _, m := range refs {
		r := httptest.NewRequest("GET", m[1], nil)
		r.Host = "127.0.0.1:8787"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || w.Body.Len() == 0 {
			t.Errorf("%s = %d with %d bytes", m[1], w.Code, w.Body.Len())
		}
	}
}
