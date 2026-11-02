package effbench

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Adapter describes how to launch one agent headless against the proxy.
// Placeholders in Args, Env values and Files: {base_url} (the proxy, in the
// wire format Proto names), {model}, {prompt}, {key} (a dummy; the proxy holds
// the real key), {ws} (workspace), {home} (the run's private HOME).
type Adapter struct {
	Name  string            `json:"name"`
	Bin   string            `json:"bin"`
	Proto string            `json:"proto"` // openai (base_url ends /v1) | anthropic (no /v1)
	Args  []string          `json:"args"`
	Env   map[string]string `json:"env,omitempty"`
	Files map[string]string `json:"files,omitempty"` // path under {home} -> content
	Stdin bool              `json:"stdin,omitempty"` // send the prompt on stdin
	Note  string            `json:"note,omitempty"`
}

// Builtin returns the shipped adapters. janetcode is exercised by this repo's
// tests; the others are templates written from each CLI's documented headless
// mode and have not been run here: adjust with --adapters.
func Builtin() []Adapter {
	return []Adapter{
		{Name: "janetcode", Bin: "janetcode", Proto: "openai",
			Args: []string{"-p", "{prompt}", "--provider", "openai", "--model", "{model}", "--yolo"},
			Env:  map[string]string{"OPENAI_BASE_URL": "{base_url}", "OPENAI_API_KEY": "{key}"}},
		{Name: "aider", Bin: "aider", Proto: "openai", Note: "template",
			Args: []string{"--yes-always", "--no-git", "--no-auto-commits", "--no-check-update", "--model", "openai/{model}", "--message", "{prompt}"},
			Env:  map[string]string{"OPENAI_API_BASE": "{base_url}", "OPENAI_API_KEY": "{key}"}},
		{Name: "opencode", Bin: "opencode", Proto: "openai", Note: "template",
			Args:  []string{"run", "-m", "eff/{model}", "{prompt}"},
			Files: map[string]string{".config/opencode/opencode.json": `{"provider":{"eff":{"npm":"@ai-sdk/openai-compatible","options":{"baseURL":"{base_url}","apiKey":"{key}"},"models":{"{model}":{}}}}}`}},
		{Name: "codex", Bin: "codex", Proto: "openai", Note: "template",
			Args: []string{"exec", "--skip-git-repo-check", "--full-auto", "-m", "{model}",
				"-c", `model_provider="eff"`, "-c", `model_providers.eff={name="eff",base_url="{base_url}",env_key="EFF_KEY",wire_api="chat"}`, "{prompt}"},
			Env: map[string]string{"EFF_KEY": "{key}"}},
		{Name: "claude", Bin: "claude", Proto: "anthropic", Note: "template; claude validates the model name against its catalog",
			Args: []string{"-p", "{prompt}", "--model", "{model}", "--dangerously-skip-permissions"},
			Env:  map[string]string{"ANTHROPIC_BASE_URL": "{base_url}", "ANTHROPIC_AUTH_TOKEN": "{key}"}},
		{Name: "pi", Bin: "pi", Proto: "openai", Note: "template",
			Args:  []string{"-p", "{prompt}", "--provider", "eff", "--model", "{model}"},
			Files: map[string]string{".pi/agent/models.json": `{"providers":{"eff":{"baseUrl":"{base_url}","api":"openai-completions","apiKey":"{key}","models":[{"id":"{model}"}]}}}`}},
	}
}

// LoadAdapters returns the builtins, overridden or extended by a JSON file
// holding an array of Adapter (matched by name). path may be empty.
func LoadAdapters(path string) ([]Adapter, error) {
	all := Builtin()
	if path == "" {
		return all, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var extra []Adapter
	if err := json.Unmarshal(b, &extra); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, e := range extra {
		if e.Name == "" || e.Bin == "" {
			return nil, fmt.Errorf("%s: adapter needs name and bin", path)
		}
		replaced := false
		for i := range all {
			if all[i].Name == e.Name {
				all[i], replaced = e, true
			}
		}
		if !replaced {
			all = append(all, e)
		}
	}
	return all, nil
}

// Available reports the resolved binary, or ok=false when it is not installed:
// a missing agent is skipped, never a failure.
func (a Adapter) Available() (path string, ok bool) {
	p, err := exec.LookPath(a.Bin)
	return p, err == nil
}

// Vars are the placeholder values for one run.
type Vars struct{ BaseURL, Model, Prompt, Key, WS, Home string }

func (v Vars) expand(s string) string {
	return strings.NewReplacer("{base_url}", v.BaseURL, "{model}", v.Model, "{prompt}", v.Prompt,
		"{key}", v.Key, "{ws}", v.WS, "{home}", v.Home).Replace(s)
}

// BaseURL is the proxy address in the wire format the adapter speaks.
func (a Adapter) BaseURL(proxy string) string {
	if a.Proto == "anthropic" {
		return proxy
	}
	return proxy + "/v1"
}

// Command expands the adapter into argv, extra environment (KEY=VALUE) and
// files to write under HOME.
func (a Adapter) Command(v Vars) (argv, env []string, files map[string]string) {
	argv = append(argv, a.Bin)
	for _, s := range a.Args {
		argv = append(argv, v.expand(s))
	}
	for k, val := range a.Env {
		env = append(env, k+"="+v.expand(val))
	}
	files = map[string]string{}
	for p, c := range a.Files {
		files[v.expand(p)] = v.expand(c)
	}
	return
}
