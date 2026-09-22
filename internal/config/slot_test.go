package config

import "testing"

func slotCfg() *Config {
	return &Config{
		Provider: "anthropic",
		Model:    "claude-opus-5",
		Providers: map[string]Provider{
			"anthropic": {Type: "anthropic", APIKey: "x"},
			"dashscope": {Type: "openai", BaseURL: "https://dashscope.example/v1", APIKey: "x"},
		},
	}
}

func TestSlotFallsBackToTheMainModel(t *testing.T) {
	c := slotCfg()
	for _, name := range SlotNames {
		prov, model := c.Slot(name)
		if model != "claude-opus-5" || prov != "anthropic" {
			t.Fatalf("unset slot %q resolved to %s/%s, want anthropic/claude-opus-5", name, prov, model)
		}
		if !c.SlotIsMain(name) {
			t.Fatalf("unset slot %q must count as the main model", name)
		}
		if got := c.SlotModel(name); got != "" {
			t.Fatalf("unset slot %q has model %q, want empty", name, got)
		}
	}
}

func TestSlotSetToTheMainModelIsTheMainModel(t *testing.T) {
	c := slotCfg()
	c.RouteCheap = "claude-opus-5"
	if !c.SlotIsMain("cheap") {
		t.Fatal("a slot set to the session's model must resolve as main")
	}
	prov, model := c.Slot("cheap")
	uprov, umodel := c.Slot("strong") // unset
	if prov != uprov || model != umodel {
		t.Fatalf("cheap=%s/%s but the unset slot is %s/%s; they must agree", prov, model, uprov, umodel)
	}
}

func TestSlotUsesWhatIsSetAndRoutesIt(t *testing.T) {
	c := slotCfg()
	c.StrongModel = "qwen/qwen3.7-plus"
	c.VisionModel = "claude-opus-5-vision"
	if c.SlotIsMain("strong") {
		t.Fatal("a filled slot is not the main model")
	}
	if prov, model := c.Slot("strong"); model != "qwen/qwen3.7-plus" || prov != "dashscope" {
		t.Fatalf("strong resolved to %s/%s, want dashscope/qwen/qwen3.7-plus", prov, model)
	}
	if prov, model := c.Slot("vision"); model != "claude-opus-5-vision" || prov != "anthropic" {
		t.Fatalf("vision resolved to %s/%s, want anthropic/claude-opus-5-vision", prov, model)
	}
}

func TestSlotUnknownNameIsEmptyAndFallsBack(t *testing.T) {
	c := slotCfg()
	if KnownSlot("wizard") {
		t.Fatal("wizard is not a slot")
	}
	if got := c.SlotModel("wizard"); got != "" {
		t.Fatalf("unknown slot model %q, want empty", got)
	}
	if _, model := c.Slot("wizard"); model != "claude-opus-5" {
		t.Fatalf("unknown slot resolved to %q, want the main model", model)
	}
	if SlotKey("wizard") != "" {
		t.Fatal("an unknown slot has no config key")
	}
}

func TestSlotKeysAreTheConfigKeys(t *testing.T) {
	want := map[string]string{"cheap": "route_cheap", "strong": "strong_model", "advisor": "advisor_model", "vision": "vision_model"}
	for _, name := range SlotNames {
		if got := SlotKey(name); got != want[name] {
			t.Fatalf("slot %q writes %q, want %q", name, got, want[name])
		}
		if !KnownSlot(name) {
			t.Fatalf("%q is listed in SlotNames but not known", name)
		}
	}
	if SlotKey("main") != "model" {
		t.Fatal("main names the session's own model key")
	}
}
