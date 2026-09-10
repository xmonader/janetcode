package rates

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sample = `{
 "sample_spec": {"max_tokens": "LEGACY", "input_cost_per_token": 0.0000},
 "gpt-4o-mini": {"input_cost_per_token": 1.5e-07, "output_cost_per_token": 6e-07, "cache_read_input_token_cost": 7.5e-08, "litellm_provider": "openai"},
 "openrouter/deepseek/deepseek-chat": {"input_cost_per_token": 1.4e-07, "output_cost_per_token": 2.8e-07},
 "anthropic/claude-sonnet-4": {"input_cost_per_token": 3e-06, "output_cost_per_token": 1.5e-05, "cache_read_input_token_cost": 3e-07, "cache_creation_input_token_cost": 3.75e-06},
 "free-thing": {"input_cost_per_token": 0, "output_cost_per_token": 0}
}`

// TestPricesComeOutPerMillionUnderAnyProviderName: the table is per
// token; janetcode prices per million. A model is found under the name
// the provider uses, under LiteLLM's prefix, or by its base name.
func TestPricesComeOutPerMillionUnderAnyProviderName(t *testing.T) {
	tb, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tb.Models["sample_spec"]; ok {
		t.Fatal("sample_spec is not a model")
	}
	if _, ok := tb.Models["free-thing"]; ok {
		t.Fatal("a model with no rates is not priced")
	}
	p := tb.PriceFor("gpt-4o-mini")
	if p == nil || p.Input != 0.15 || p.Output != 0.6 || p.CacheRead != 0.075 {
		t.Fatalf("gpt-4o-mini = %+v", p)
	}
	if p := tb.PriceFor("deepseek/deepseek-chat"); p == nil || p.Input != 0.14 {
		t.Fatalf("openrouter prefix lookup = %+v", p)
	}
	if p := tb.PriceFor("openrouter/Anthropic/claude-sonnet-4"); p == nil || p.CacheWrite != 3.75 {
		t.Fatalf("base-name lookup under a vendor prefix = %+v", p)
	}
	if tb.PriceFor("nobody/has-this") != nil || (*Table)(nil).PriceFor("x") != nil {
		t.Fatal("unknown model must be unpriced, and a nil table safe")
	}
}

// TestCacheLoadAndFreshness: Load reads whatever is cached; Fresh is
// false past the TTL so a refresh is attempted, and true just after one.
func TestCacheLoadAndFreshness(t *testing.T) {
	dir := t.TempDir()
	if Load(dir) != nil || Fresh(dir) {
		t.Fatal("no cache yet")
	}
	p := CachePath(dir)
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	if tb := Load(dir); tb == nil || tb.PriceFor("gpt-4o-mini") == nil || !Fresh(dir) {
		t.Fatal("cached table not loaded or not fresh")
	}
	old := time.Now().Add(-2 * TTL)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	if Fresh(dir) || Load(dir) == nil {
		t.Fatal("a stale cache is still loaded, but not fresh")
	}
	_ = filepath.Base(p)
}
