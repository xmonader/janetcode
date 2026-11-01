package jev

import (
	"context"
	"os"
	"testing"
	"time"
)

// The live smoke: one decisions call over the OpenRouter route, the
// same three shapes the fake-server tests cover against a real model.
// Ambient credentials are not consent to spend credits on a test run.
func TestLiveOpenRouterDecisions(t *testing.T) {
	if os.Getenv("JANETCODE_JEV_LIVE") != "1" {
		t.Skip("set JANETCODE_JEV_LIVE=1 with OPENROUTER_API_KEY to run the live smoke")
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY is not set")
	}
	c := &Client{Key: key, Model: "~typesafe/jev-latest", Timeout: 30 * time.Second}
	res, err := c.Evaluate(context.Background(), "Failing check: grep -q ok a.txt. Output: no such file.",
		[]Question{
			{ID: "urgent", Kind: Noul, Instructions: "Is this failure blocking everything?"},
			{ID: "class", Kind: Choice, Instructions: "What kind?",
				Options: map[string]string{"patch_caused": "the change", "environmental": "the environment"}},
			{ID: "severity", Kind: Score, Instructions: "How bad?", Levels: []string{"low", "mid", "high"}},
		})
	if err != nil {
		t.Fatalf("live call: %v", err)
	}
	// The contract the reference clients verify: versioned model id in
	// the response, every answer typed, probabilities within range.
	if res.Model == "" {
		t.Fatal("no model id in the response")
	}
	if _, ok := res.NoulAt("urgent"); !ok {
		t.Fatalf("urgent noul missing: %+v", res.Answers)
	}
	ch, conf, ok := res.ChoiceAt("class")
	if !ok || ch == "" || conf <= 0 {
		t.Fatalf("class = %q %v %v", ch, conf, ok)
	}
	t.Logf("model %s · urgent noul ok · class %s (conf %.2f) · %d tokens in · %v",
		res.Model, ch, conf, res.TokensIn, res.Latency)
	if res.TokensIn <= 0 {
		t.Fatal("no usage reported")
	}
}
