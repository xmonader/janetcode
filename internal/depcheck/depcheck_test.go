package depcheck

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEcosystem(t *testing.T) {
	for p, want := range map[string]string{
		"go.mod": Go, "sub/package.json": NPM, "requirements.txt": PyPI, "requirements-dev.txt": PyPI,
		"go.sum": "", "package-lock.json": "", "main.go": "", "requirements.md": "",
	} {
		if got := Ecosystem(p); got != want {
			t.Errorf("Ecosystem(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestAddedGoMod(t *testing.T) {
	before := "module m\n\ngo 1.22\n\nrequire (\n\tgithub.com/a/b v1.0.0\n\tgolang.org/x/sys v0.1.0 // indirect\n)\n"
	after := before + "require github.com/c/d v0.2.0\nrequire (\n\tgithub.com/e/f v1.1.1 // pinned\n\tgithub.com/g/h v1.0.0 // indirect\n)\n"
	if got := Added(Go, []byte(before), []byte(after)); !reflect.DeepEqual(got, []string{"github.com/c/d", "github.com/e/f"}) {
		t.Fatalf("Added = %v", got)
	}
	if got := Added(Go, []byte(after), []byte(after)); len(got) != 0 {
		t.Fatalf("unchanged manifest added %v", got)
	}
}

func TestAddedPackageJSON(t *testing.T) {
	before := `{"dependencies":{"react":"^18"}}`
	after := `{"dependencies":{"react":"^18","left-padx":"1.0.0","local":"file:../x","gh":"github:a/b","alias":"npm:other@1"},"devDependencies":{"@types/nodex":"^1"}}`
	got := Added(NPM, []byte(before), []byte(after))
	if !reflect.DeepEqual(got, []string{"@types/nodex", "alias", "left-padx"}) {
		t.Fatalf("Added = %v", got)
	}
	if got := Added(NPM, nil, []byte("{not json")); got != nil {
		t.Fatalf("broken JSON added %v", got)
	}
}

func TestAddedRequirements(t *testing.T) {
	before := "requests==2.31\n"
	after := "requests==2.31\n# comment\n-r base.txt\n-e .\nFlask_Login>=0.6 ; python_version>'3'\ngit+https://x/y.git\nnumpy[extra]~=1.26  # pinned\n"
	if got := Added(PyPI, []byte(before), []byte(after)); !reflect.DeepEqual(got, []string{"flask-login", "numpy"}) {
		t.Fatalf("Added = %v", got)
	}
}

type fakeReg struct {
	infos map[string]Info
	err   error
	asked []string
}

func (f *fakeReg) Lookup(_ context.Context, _, name string) (Info, error) {
	f.asked = append(f.asked, name)
	return f.infos[name], f.err
}

func TestCheck(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	reg := &fakeReg{infos: map[string]Info{
		"old":     {Exists: true, FirstPublished: now.AddDate(-3, 0, 0)},
		"young":   {Exists: true, FirstPublished: now.AddDate(0, 0, -5)},
		"undated": {Exists: true},
	}}
	fs := Check(context.Background(), reg, NPM, []string{"old", "young", "ghost", "undated", "private"}, now, func(n string) bool { return n == "private" })
	if len(fs) != 2 || fs[0].Name != "young" || fs[0].Age != 5*24*time.Hour || !fs[1].Missing || fs[1].Name != "ghost" {
		t.Fatalf("findings = %+v", fs)
	}
	if contains(reg.asked, "private") {
		t.Error("a skipped name was looked up")
	}
	note := Note("package.json", fs)
	for _, want := range []string{"[dependency check] package.json adds:", "young: first published 5 days ago", "ghost: not found in the public npm registry"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if Note("x", nil) != "" {
		t.Error("no findings, still a note")
	}
}

// Fails open: an erroring registry or an ended context flags nothing.
func TestCheckFailsOpen(t *testing.T) {
	reg := &fakeReg{err: errors.New("dial tcp: no route to host")}
	if fs := Check(context.Background(), reg, Go, []string{"x"}, time.Now(), nil); len(fs) != 0 {
		t.Fatalf("an unreachable registry produced %+v", fs)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reg = &fakeReg{}
	if fs := Check(ctx, reg, Go, []string{"x", "y"}, time.Now(), nil); len(fs) != 0 || len(reg.asked) != 0 {
		t.Fatalf("after the deadline: %+v asked %v", fs, reg.asked)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// fakeRegistries serves the three registries' shapes from a local
// server: no test here reaches the network.
func fakeRegistries(t *testing.T) *HTTPRegistry {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/github.com/!burnt!sushi/toml/@v/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "v1.2.0\nv0.10.0\nv0.9.1\nv1.0.0-rc1\n")
	})
	mux.HandleFunc("/github.com/!burnt!sushi/toml/@v/v0.9.1.info", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Version":"v0.9.1","Time":"2013-03-01T00:00:00Z"}`)
	})
	mux.HandleFunc("/github.com/x/untagged/@v/list", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/github.com/x/untagged/@latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"Version":"v0.0.0-2026","Time":"2026-10-01T00:00:00Z"}`)
	})
	mux.HandleFunc("/github.com/x/flaky/@v/list", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusServiceUnavailable)
	})
	npm := func(w http.ResponseWriter, r *http.Request) {
		// A scoped name is one path segment, its slash escaped.
		if r.URL.EscapedPath() != "/@scope%2Fpkg" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"time":{"created":"2026-10-01T00:00:00.000Z","modified":"2026-10-02T00:00:00Z"}}`)
	}
	mux.HandleFunc("/pypi/requests/json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"releases":{"2.0":[{"upload_time_iso_8601":"2013-09-24T00:00:00Z"}],"0.2":[{"upload_time_iso_8601":"2011-02-14T00:00:00.123Z"}],"9.9":[]}}`)
	})
	mux.HandleFunc("/", npm)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &HTTPRegistry{Client: srv.Client(), GoProxy: srv.URL, NPM: srv.URL, PyPI: srv.URL}
}

func TestHTTPRegistry(t *testing.T) {
	r := fakeRegistries(t)
	ctx := context.Background()
	day := func(s string) time.Time { tm, _ := time.Parse("2006-01-02", s); return tm }

	info, err := r.Lookup(ctx, Go, "github.com/BurntSushi/toml")
	if err != nil || !info.Exists || !info.FirstPublished.Equal(day("2013-03-01")) {
		t.Errorf("go tagged: %+v %v", info, err)
	}
	if info, err := r.Lookup(ctx, Go, "github.com/x/untagged"); err != nil || !info.Exists || !info.FirstPublished.IsZero() {
		t.Errorf("go untagged: %+v %v", info, err)
	}
	if info, err := r.Lookup(ctx, Go, "github.com/x/ghost"); err != nil || info.Exists {
		t.Errorf("go missing: %+v %v", info, err)
	}
	if _, err := r.Lookup(ctx, Go, "github.com/x/flaky"); err == nil {
		t.Error("a 503 was taken as an answer")
	}
	if info, err := r.Lookup(ctx, NPM, "@scope/pkg"); err != nil || !info.Exists || info.FirstPublished.Format("2006-01-02") != "2026-10-01" {
		t.Errorf("npm scoped: %+v %v", info, err)
	}
	if info, err := r.Lookup(ctx, NPM, "nope"); err != nil || info.Exists {
		t.Errorf("npm missing: %+v %v", info, err)
	}
	if info, err := r.Lookup(ctx, PyPI, "requests"); err != nil || info.FirstPublished.Format("2006-01-02") != "2011-02-14" {
		t.Errorf("pypi: %+v %v", info, err)
	}
}

func TestSemverLess(t *testing.T) {
	for _, c := range [][2]string{{"v0.9.1", "v0.10.0"}, {"v1.0.0-rc1", "v1.0.0"}, {"v1.2.3", "v2.0.0"}, {"v1.0.0-alpha", "v1.0.0-beta"}} {
		if !semverLess(c[0], c[1]) || semverLess(c[1], c[0]) {
			t.Errorf("semverLess(%s, %s) wrong", c[0], c[1])
		}
	}
}

func TestGoPrivateAndProxy(t *testing.T) {
	env := map[string]string{"GOPRIVATE": "corp.example.com,github.com/acme/*", "GOPROXY": "https://goproxy.corp,direct"}
	get := func(k string) string { return env[k] }
	for mod, want := range map[string]bool{
		"corp.example.com/lib": true, "github.com/acme/tools/x": true, "github.com/other/x": false, "corp.example.com.evil/x": false,
	} {
		if got := GoPrivate(get, mod); got != want {
			t.Errorf("GoPrivate(%s) = %v", mod, got)
		}
	}
	if p := GoProxyURL(get); p != "https://goproxy.corp" {
		t.Errorf("GoProxyURL = %q", p)
	}
	env["GOPROXY"] = "off"
	if p := GoProxyURL(get); p != "off" {
		t.Errorf("GOPROXY=off -> %q", p)
	}
	env["GOPROXY"] = ""
	if p := GoProxyURL(get); p != "" {
		t.Errorf("unset -> %q", p)
	}
}

func TestEscapeModule(t *testing.T) {
	if got := escapeModule("github.com/BurntSushi/toml"); got != "github.com/!burnt!sushi/toml" {
		t.Fatal(got)
	}
}
