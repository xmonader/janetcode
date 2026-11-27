package agent

import "testing"

func TestReplayModelKnowsInklingAndConfiguredGlobs(t *testing.T) {
	for _, id := range []string{"thinkingmachines/inkling-2-small", "inkling-2-mini", "INKLING"} {
		if !ReplayModel(id) {
			t.Errorf("%s: want replay", id)
		}
	}
	for _, id := range []string{"kimi-for-coding", "deepseek/deepseek-v4.1-flash", "openai/gpt-oss-20b", ""} {
		if ReplayModel(id) {
			t.Errorf("%s: want no replay", id)
		}
	}
	if !ReplayModel("z-ai/glm-5", "glm-*") {
		t.Error("configured glob ignored")
	}
}

func TestReplayFollowsTheSessionModel(t *testing.T) {
	a := &Agent{Off: map[string]bool{}}
	a.UpdateSettings(func(s *Settings) { s.Model = "thinkingmachines/inkling-2-small" })
	if !a.replayOpenAI() {
		t.Fatal("inkling: want replay")
	}
	a.UpdateSettings(func(s *Settings) { s.Model = "kimi-for-coding" })
	if a.replayOpenAI() {
		t.Fatal("kimi: want no replay")
	}
	a.ReasoningReplay = true
	if !a.replayOpenAI() {
		t.Fatal("reasoning_replay: want replay")
	}
	a.Off["reasoning"] = true
	if a.replayOpenAI() {
		t.Fatal("the switch must win")
	}
}
