package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
)

func TestWorkspaceCheckAndApply(t *testing.T) {
	s, cd, here, sib := cdServer(t)
	h := s.handler()
	base := filepath.Dir(here)
	file := filepath.Join(base, "afile")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(base, "fresh", "deep")
	type result struct {
		Path    string `json:"path"`
		Exists  bool   `json:"exists"`
		IsDir   bool   `json:"is_dir"`
		Created bool   `json:"created"`
	}
	cases := []struct {
		name, route, body string
		want              int
		check             func(t *testing.T, r result)
	}{
		{"existing dir", "check", `{"path":"` + sib + `"}`, 200, func(t *testing.T, r result) {
			if !r.Exists || !r.IsDir || r.Created {
				t.Fatalf("%+v", r)
			}
		}},
		{"missing dir is only reported", "check", `{"path":"` + fresh + `"}`, 200, func(t *testing.T, r result) {
			if r.Exists {
				t.Fatalf("%+v", r)
			}
			if _, err := os.Stat(fresh); err == nil {
				t.Fatal("check created the folder without being asked")
			}
		}},
		{"a file is refused", "check", `{"path":"` + file + `"}`, 400, nil},
		{"relative is refused", "check", `{"path":"sibling"}`, 400, nil},
		{"empty is refused", "check", `{"path":" "}`, 400, nil},
		{"apply missing without create", "apply", `{"path":"` + fresh + `"}`, 404, nil},
		{"apply a file", "apply", `{"path":"` + file + `"}`, 400, nil},
		{"apply moves the session", "apply", `{"path":"` + sib + `"}`, 200, func(t *testing.T, r result) {
			if cd.Dir() != sib || s.cwd() != sib {
				t.Fatalf("cd = %q, server = %q", cd.Dir(), s.cwd())
			}
		}},
		{"create on confirm", "apply", `{"path":"` + fresh + `","create":true}`, 200, func(t *testing.T, r result) {
			if !r.Created || cd.Dir() != fresh {
				t.Fatalf("%+v dir=%q", r, cd.Dir())
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := postJSON(t, h, "/api/workspace/"+c.route, c.body)
			if w.Code != c.want {
				t.Fatalf("= %d %s, want %d", w.Code, w.Body, c.want)
			}
			if c.check != nil {
				var r result
				_ = json.Unmarshal(w.Body.Bytes(), &r)
				c.check(t, r)
			}
		})
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	if w := postJSON(t, h, "/api/workspace/apply", `{"path":"`+here+`"}`); w.Code != http.StatusConflict {
		t.Errorf("apply while running = %d", w.Code)
	}
}

func TestResolveWorkspacePathTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for in, want := range map[string]string{"~": home, "~/proj": filepath.Join(home, "proj")} {
		if got, err := resolveWorkspacePath(in); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := resolveWorkspacePath("~other/x"); err == nil {
		t.Error("~user was accepted")
	}
}

func TestConnectTestKeyEnvAndDefault(t *testing.T) {
	s, path := firstRun(t)
	t.Setenv("JANET_TEST_KEY", "good")
	url := `"url":"https://coding-intl.dashscope.aliyuncs.com/v1"`
	// Test before saving: reaches the provider, writes nothing.
	ok := connectCall(t, s, `{"action":"test","name":"dashscope-coding",`+url+`,"keys":"good"}`)
	if ok["ok"] != true || ok["models"] != float64(2) {
		t.Fatalf("test = %v", ok)
	}
	bad := connectCall(t, s, `{"action":"test","name":"dashscope-coding",`+url+`,"keys":"nope"}`)
	if e, _ := bad["error"].(map[string]any); e["headline"] != "Authentication failed" {
		t.Fatalf("bad test = %v", bad)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("testing a connection wrote the config")
	}
	// An unset variable is named, not tried.
	unset := connectCall(t, s, `{"action":"test","name":"dashscope-coding",`+url+`,"key_env":"JANET_TEST_NOPE"}`)
	if e, _ := unset["error"].(map[string]any); !strings.Contains(e["reason"].(string), "JANET_TEST_NOPE is not set") {
		t.Fatalf("unset env = %v", unset)
	}
	// Env var instead of a key; saved without being the default.
	done := connectCall(t, s, `{"action":"connect","name":"dashscope-coding",`+url+`,"key_env":"JANET_TEST_KEY","model":"qwen3.7-plus","make_default":false}`)
	if done["ok"] != true {
		t.Fatalf("connect = %v", done)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "good") || !strings.Contains(string(data), `"key_env": "JANET_TEST_KEY"`) {
		t.Fatalf("the secret must stay out of the file: %s", data)
	}
	var saved map[string]json.RawMessage
	_ = json.Unmarshal(data, &saved)
	if _, has := saved["provider"]; has {
		t.Fatalf("make_default=false still wrote the default: %s", data)
	}
	// The list names the connected provider and never carries a key.
	w := httpGet(t, s, "/api/connect")
	if !strings.Contains(w, `"connected":true`) || !strings.Contains(w, `"key_env":"JANET_TEST_KEY"`) || strings.Contains(w, "good") {
		t.Fatalf("providers = %s", w)
	}
	// Names that are not variables are refused.
	cfg := config.Provider{KeyEnv: "JANET_TEST_KEY"}
	if err := cfg.UseEnvKey("1BAD", os.Getenv); err == nil {
		t.Fatal("invalid variable name accepted")
	}
}

func TestPermOfWarningsAndSource(t *testing.T) {
	s, _, here, _ := cdServer(t)
	cases := []struct {
		name    string
		call    agent.ToolCall
		warns   []string
		source  string
		outside bool
	}{
		{"hook ask", agent.ToolCall{ID: "1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`), Warning: "a hook from this repository rewrote this call's input", Source: "hook"},
			[]string{"a hook from this repository rewrote this call's input"}, "hook", false},
		{"two causes split", agent.ToolCall{ID: "2", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`), Warning: "a; b"}, []string{"a", "b"}, "", false},
		{"path outside", agent.ToolCall{ID: "3", Name: "write_file", Input: json.RawMessage(`{"path":"/etc/hosts"}`)}, nil, "", true},
		{"relative escape", agent.ToolCall{ID: "4", Name: "edit_file", Input: json.RawMessage(`{"file_path":"../x.go"}`)}, nil, "", true},
		{"inside", agent.ToolCall{ID: "5", Name: "edit_file", Input: json.RawMessage(`{"path":"sub/x.go"}`)}, nil, "", false},
		{"inside absolute", agent.ToolCall{ID: "6", Name: "edit_file", Input: json.RawMessage(`{"path":"` + filepath.Join(here, "x.go") + `"}`)}, nil, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := s.permOf(c.call)
			var out []string
			for _, w := range p.Warnings {
				if strings.Contains(w, "outside the workspace") {
					if !c.outside {
						t.Fatalf("unexpected outside warning %q", w)
					}
					continue
				}
				out = append(out, w)
			}
			if c.outside && len(p.Warnings) == len(out) {
				t.Fatalf("no outside-workspace warning in %v", p.Warnings)
			}
			if strings.Join(out, "|") != strings.Join(c.warns, "|") || p.Source != c.source {
				t.Fatalf("warnings %v source %q, want %v %q", out, p.Source, c.warns, c.source)
			}
			if c.call.Warning != "" && p.Warning != c.call.Warning {
				t.Fatalf("Warning line changed: %q", p.Warning)
			}
		})
	}
}

func httpGet(t *testing.T, s *server, path string) string {
	t.Helper()
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Body.String()
}
