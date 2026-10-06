package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/weburl"
)

// Each backend sends its own request shape and its key where the API
// expects it, and its reply's field names come back as results.
func TestAPIBackends(t *testing.T) {
	cases := []struct {
		provider, keyHeader, keyValue, method, reply string
		checkBody                                    func(map[string]any) bool
	}{
		{"tavily", "Authorization", "Bearer k1", "POST", `{"results":[{"title":"T","url":"https://a.example/1","content":"c  one"}]}`,
			func(b map[string]any) bool { return b["query"] == "go generics" && b["max_results"] == 3.0 }},
		{"serper", "X-Api-Key", "k1", "POST", `{"organic":[{"title":"T","link":"https://a.example/1","snippet":"c one"}]}`,
			func(b map[string]any) bool { return b["q"] == "go generics" && b["num"] == 3.0 }},
		{"exa", "X-Api-Key", "k1", "POST", `{"results":[{"title":"T","url":"https://a.example/1","text":"c\n one"}]}`,
			func(b map[string]any) bool { return b["query"] == "go generics" && b["numResults"] == 3.0 }},
		{"searxng", "", "", "GET", `{"results":[{"title":"T","url":"https://a.example/1","content":"c one"},{"title":"no url"}]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.provider, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != c.method {
					t.Errorf("method %s", r.Method)
				}
				if c.keyHeader != "" && r.Header.Get(c.keyHeader) != c.keyValue {
					t.Errorf("%s = %q", c.keyHeader, r.Header.Get(c.keyHeader))
				}
				if c.checkBody != nil {
					var b map[string]any
					raw, _ := io.ReadAll(r.Body)
					if json.Unmarshal(raw, &b) != nil || !c.checkBody(b) {
						t.Errorf("body = %s", raw)
					}
				}
				if c.provider == "searxng" && (r.URL.Path != "/search" || r.URL.Query().Get("format") != "json" || r.URL.Query().Get("q") != "go generics") {
					t.Errorf("url = %s", r.URL)
				}
				io.WriteString(w, c.reply)
			}))
			defer srv.Close()
			ws := WebSearch{Config: weburl.SearchConfig{Provider: c.provider, Key: "k1", URL: srv.URL}}
			got, err := ws.Search(context.Background(), "go generics", 3)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].URL != "https://a.example/1" || got[0].Title != "T" || got[0].Snippet != "c one" {
				t.Fatalf("results = %+v", got)
			}
		})
	}
}

// The key comes from the provider's variable when the config has none,
// and never follows a redirect to another host.
func TestAPIKeyFromEnvAndRedirectGuard(t *testing.T) {
	leak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("redirect followed; key %q reached another host", r.Header.Get("X-Api-Key"))
	}))
	defer leak.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "from-env" {
			t.Errorf("key = %q", r.Header.Get("X-Api-Key"))
		}
		http.Redirect(w, r, leak.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	t.Setenv("SERPER_API_KEY", "from-env")
	_, err := WebSearch{Config: weburl.SearchConfig{Provider: "serper", URL: srv.URL}}.Search(context.Background(), "q", 3)
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("SERPER_API_KEY", "")
	if _, err := (WebSearch{Config: weburl.SearchConfig{Provider: "serper", URL: srv.URL}}).Search(context.Background(), "q", 3); err == nil || !strings.Contains(err.Error(), "SERPER_API_KEY") {
		t.Fatalf("no key: %v", err)
	}
}
