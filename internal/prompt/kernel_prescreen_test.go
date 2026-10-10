package prompt

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKernelPrescreenEffbench is the prefix kernel's offline pre-screen,
// read from what effbench's recording proxy kept of each request (it
// keeps sizes and usage, not bodies): how much of a run's first call the
// provider already served from cache, how much of every run's input the
// first calls are, and whether the tool list — the part above the system
// prompt — was byte-for-byte the same size in every run. Skipped unless
// JANETCODE_KERNEL_PRESCREEN names a campaign glob:
//
//	JANETCODE_KERNEL_PRESCREEN='docs/benchmarks/effbench/runs/jc-mimo-r1[0-3]/janetcode' go test ./internal/prompt -run KernelPrescreen -v
func TestKernelPrescreenEffbench(t *testing.T) {
	pattern := os.Getenv("JANETCODE_KERNEL_PRESCREEN")
	if pattern == "" {
		t.Skip("JANETCODE_KERNEL_PRESCREEN not set")
	}
	dirs, _ := filepath.Glob(pattern)
	type call struct {
		Path         string `json:"path"`
		ToolsBytes   int    `json:"tools_bytes"`
		PromptTokens int    `json:"prompt_tokens"`
		CachedTokens int    `json:"cached_tokens"`
	}
	var runs, firsts, firstPrompt, firstCached, allPrompt, allCached int
	toolSizes := map[int]int{}
	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*", "proxy_calls.jsonl"))
		for _, f := range files {
			fh, err := os.Open(f)
			if err != nil {
				continue
			}
			var calls []call
			sc := bufio.NewScanner(fh)
			sc.Buffer(make([]byte, 1<<20), 1<<24)
			for sc.Scan() {
				var c call
				if json.Unmarshal(sc.Bytes(), &c) == nil && strings.HasSuffix(c.Path, "completions") {
					calls = append(calls, c)
				}
			}
			fh.Close()
			if len(calls) == 0 {
				continue
			}
			runs++
			toolSizes[calls[0].ToolsBytes]++
			if calls[0].PromptTokens > 0 {
				firsts++
				firstPrompt += calls[0].PromptTokens
				firstCached += calls[0].CachedTokens
			}
			for _, c := range calls {
				allPrompt += c.PromptTokens
				allCached += c.CachedTokens
			}
		}
	}
	if runs == 0 || firsts == 0 || allPrompt == 0 {
		t.Skip("no recorded runs with usage under " + pattern)
	}
	t.Logf("%d run(s); tool list sizes %v", runs, toolSizes)
	t.Logf("first calls with usage: %d; %d of %d prompt tokens from cache (%.0f%%)", firsts, firstCached, firstPrompt, 100*float64(firstCached)/float64(firstPrompt))
	t.Logf("all calls: %d of %d prompt tokens from cache (%.1f%%); first calls are %.2f%% of all input, %.2f%% of it uncached",
		allCached, allPrompt, 100*float64(allCached)/float64(allPrompt),
		100*float64(firstPrompt)/float64(allPrompt), 100*float64(firstPrompt-firstCached)/float64(allPrompt))
}
