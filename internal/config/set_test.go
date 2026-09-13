package config

import "testing"

func TestApplyKVThinking(t *testing.T) {
	cfg := &Config{}
	v, err := ApplyKV(cfg, "thinking", "false")
	if err != nil {
		t.Fatal(err)
	}
	if v != false || cfg.Thinking == nil || *cfg.Thinking {
		t.Fatalf("got %v cfg=%v", v, cfg.Thinking)
	}
	if _, err := ApplyKV(cfg, "nope", "1"); err == nil {
		t.Fatal("unknown key should fail")
	}
}
