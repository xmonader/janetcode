// The grok harness: a probe that checks the CLI is usable before a batch
// commits to it, and the temporary GROK_HOME it needs.
package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/shell"
)

func probeGrok(ctx context.Context, cfg runConfig, tasks []eval.Task) ([]eval.BenchTaskResult, string, bool) {
	home, err := os.MkdirTemp("", "grok-home-dashscope-")
	if err != nil {
		rs := eval.UnavailableResults("grok", "mkdir GROK_HOME: "+err.Error(), "grok", err.Error(), tasks)
		return rs, err.Error(), false
	}
	toml := fmt.Sprintf(`[model."%s"]
model = %q
base_url = %q
name = "Qwen Dashscope"
env_key = "OPENAI_API_KEY"
api_backend = "chat_completions"
context_window = 128000
`, cfg.Model, cfg.Model, cfg.BaseURL)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(toml), 0o644); err != nil {
		rs := eval.UnavailableResults("grok", err.Error(), "grok", err.Error(), tasks)
		return rs, err.Error(), false
	}
	pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(pctx, "grok", "-p", "Reply with the single word pong and nothing else.",
		"--always-approve", "--output-format", "json", "--model", cfg.Model, "--max-turns", "2")
	shell.Supervise(cmd)
	cmd.Dir = home // auto-approve must never run in the real repository
	cmd.Env = childEnv(cfg, map[string]string{
		"GROK_HOME":         home,
		"OPENAI_API_KEY":    cfg.Key,
		"OPENAI_BASE_URL":   cfg.BaseURL,
		"DASHSCOPE_API_KEY": cfg.Key,
		"XAI_API_KEY":       "",
	})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	out := stdout.String() + "\n" + stderr.String()
	low := strings.ToLower(out)
	model := strings.ToLower(cfg.Model)
	aimed := strings.Contains(low, model)
	if strings.Contains(low, `"model": "grok-4`) && !strings.Contains(low, model) {
		aimed = false
	}
	if strings.Contains(low, cfg.BaseURL) || strings.Contains(low, "dashscope") {
		aimed = true
	}
	if err != nil && (strings.Contains(low, "unknown model") || strings.Contains(low, "unexpected argument")) {
		aimed = false
	}
	if !aimed {
		reason := "grok cannot be aimed at " + cfg.Model + " on " + cfg.BaseURL + " without using xAI; probe output did not confirm the custom model"
		if err != nil {
			reason += ": " + err.Error()
		}
		excerpt := trimCap(out, 2000)
		note := fmt.Sprintf("Probe command `grok -p ... --model %s` with GROK_HOME custom `[model.%s]` (env_key only, no key on disk).\n\n```\n%s\n```\n", cfg.Model, cfg.Model, excerpt)
		os.RemoveAll(home)
		return eval.UnavailableResults("grok", reason, "grok -p --model "+cfg.Model+" --output-format json", excerpt, tasks), note, false
	}
	// keep GROK_HOME for the real run; stash on cfg via pointer... cfg is a copy.
	// Re-create path is stored in a package-level so invoke can see it.
	grokHomeMu.Lock()
	grokHomePath = home
	grokHomeMu.Unlock()
	return nil, fmt.Sprintf("GROK_HOME custom model %s -> %s (env_key=OPENAI_API_KEY, key not written).\n", cfg.Model, cfg.BaseURL), true
}

var (
	grokHomeMu   sync.Mutex
	grokHomePath string
)

// cleanupGrokHome removes the temporary GROK_HOME after the run.
func cleanupGrokHome() {
	grokHomeMu.Lock()
	defer grokHomeMu.Unlock()
	if grokHomePath != "" {
		os.RemoveAll(grokHomePath)
		grokHomePath = ""
	}
}
