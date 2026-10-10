package provenance

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// fpModel answers each canary from a table keyed by a word of its prompt.
type fpModel struct {
	answers map[string]string
	fail    bool
}

func (m fpModel) Name() string { return "fake" }

func (m fpModel) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if m.fail {
		return nil, errors.New("down")
	}
	prompt := req.Messages[0].Text()
	for k, a := range m.answers {
		if strings.Contains(prompt, k) {
			return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, a)}, nil
		}
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "I am not sure.")}, nil
}

// fpClaude is a model that answers every canary right and tersely.
func fpClaude() fpModel {
	return fpModel{answers: map[string]string{
		"company": "Anthropic", "37 * 43": "1591", "backwards": "tnirpregnif",
		"strawberry": "3", "Australia": "Canberra", "next number": "42",
	}}
}

// fpKimi names its own maker and pads its answers.
func fpKimi() fpModel {
	return fpModel{answers: map[string]string{
		"company": "Moonshot AI", "37 * 43": "The answer is **1591**.", "backwards": "Reversed, it is: tnirpregnif",
		"strawberry": "There are 3 r's.", "Australia": "The capital is Canberra.", "next number": "42",
	}}
}

func fpTake(t *testing.T, m fpModel) Fingerprint {
	t.Helper()
	fp, err := Take(context.Background(), m, "broker/claude-sonnet-4-5", "claude-sonnet-4-5")
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestFingerprintTake(t *testing.T) {
	fp := fpTake(t, fpClaude())
	if fp.Maker != "anthropic" || len(fp.Replies) != len(Canaries) {
		t.Fatalf("fp = %+v", fp)
	}
	for _, c := range Canaries[1:] {
		if !fp.Correct[c.ID] || !fp.Terse[c.ID] {
			t.Fatalf("%s: correct=%v terse=%v reply=%q", c.ID, fp.Correct[c.ID], fp.Terse[c.ID], fp.Replies[c.ID])
		}
	}
	k := fpTake(t, fpKimi())
	if !k.Correct["multiply"] || k.Terse["multiply"] {
		t.Fatalf("a padded right answer: correct=%v terse=%v", k.Correct["multiply"], k.Terse["multiply"])
	}
}

// The same model fingerprinted twice is the same model.
func TestFingerprintSameModelUnchanged(t *testing.T) {
	if v := Compare(fpTake(t, fpClaude()), fpTake(t, fpClaude())); v.Changed || len(v.Reasons) != 0 {
		t.Fatalf("verdict = %+v", v)
	}
}

// The 2026-10-01 swap: a route that answered as Claude now answers as
// Kimi.
func TestFingerprintSwapDetected(t *testing.T) {
	v := Compare(fpTake(t, fpClaude()), fpTake(t, fpKimi()))
	if !v.Changed || !strings.Contains(v.String(), "moonshot") || !strings.Contains(v.String(), "changed shape") {
		t.Fatalf("verdict = %+v", v)
	}
}

// A sampled reply flipping on its own is noise: one lost answer is
// mentioned, not reported as a swap.
func TestFingerprintOneFlipIsNoise(t *testing.T) {
	m := fpClaude()
	m.answers["strawberry"] = "2"
	v := Compare(fpTake(t, fpClaude()), fpTake(t, m))
	if v.Changed || !strings.Contains(v.String(), "count") {
		t.Fatalf("verdict = %+v", v)
	}
}

// Two lost answers on the same route is a different model — the
// quantised build served under the full model's name.
func TestFingerprintQuantisationDetected(t *testing.T) {
	m := fpClaude()
	m.answers["strawberry"] = "2"
	m.answers["37 * 43"] = "1581"
	if v := Compare(fpTake(t, fpClaude()), fpTake(t, m)); !v.Changed {
		t.Fatalf("verdict = %+v", v)
	}
}

// A model that will not say who made it gives no maker, which is not a
// different maker.
func TestFingerprintNoMakerIsNotAMismatch(t *testing.T) {
	m := fpClaude()
	m.answers["company"] = "I'm an AI assistant."
	if v := Compare(fpTake(t, fpClaude()), fpTake(t, m)); v.Changed {
		t.Fatalf("verdict = %+v", v)
	}
	if _, bad := CheckMaker("claude-sonnet-4-5", fpTake(t, m)); bad {
		t.Fatal("no maker named, but a mismatch reported")
	}
}

// Canaries only one side answered are not compared.
func TestFingerprintComparesOnlyShared(t *testing.T) {
	stored := fpTake(t, fpClaude())
	now := fpTake(t, fpKimi())
	now.Maker = "anthropic"
	for _, id := range []string{"multiply", "reverse", "count", "capital"} {
		delete(now.Replies, id)
	}
	if v := Compare(stored, now); v.Changed {
		t.Fatalf("verdict = %+v", v)
	}
}

// A model naming a maker other than its name's vendor is caught with
// nothing stored; a host or an unknown name decides nothing.
func TestCheckMaker(t *testing.T) {
	if why, bad := CheckMaker("anthropic/claude-sonnet-4.5", fpTake(t, fpKimi())); !bad || !strings.Contains(why, "moonshot") {
		t.Fatalf("why = %q bad = %v", why, bad)
	}
	if _, bad := CheckMaker("my-private-model", fpTake(t, fpKimi())); bad {
		t.Fatal("an unknown requested vendor gave a verdict")
	}
}

// The session check stores the first fingerprint, says nothing while the
// route behaves, reports a swap, and keeps the reference through it.
func TestFingerprintSession(t *testing.T) {
	s := FingerprintStore{Dir: t.TempDir()}
	ctx := context.Background()
	route := "ai02/claude-sonnet-4-5"
	notice, changed, err := s.Session(ctx, fpClaude(), route, "claude-sonnet-4-5")
	if err != nil || changed || !strings.Contains(notice, "recorded") {
		t.Fatalf("first: %q %v %v", notice, changed, err)
	}
	if notice, changed, err = s.Session(ctx, fpClaude(), route, "claude-sonnet-4-5"); err != nil || changed || notice != "" {
		t.Fatalf("same: %q %v %v", notice, changed, err)
	}
	notice, changed, err = s.Session(ctx, fpKimi(), route, "claude-sonnet-4-5")
	if err != nil || !changed || !strings.Contains(notice, "delete") {
		t.Fatalf("swap: %q %v %v", notice, changed, err)
	}
	stored, ok, _ := s.Load(route)
	if !ok || stored.Maker != "anthropic" {
		t.Fatalf("the reference was overwritten by the swap: %+v", stored)
	}
	// A route whose very first fingerprint already disagrees with its
	// name is reported and not taken as the reference.
	notice, changed, _ = s.Session(ctx, fpKimi(), "other/claude-opus-4-1", "claude-opus-4-1")
	if !changed || !strings.Contains(notice, "moonshot") {
		t.Fatalf("first-use mismatch: %q %v", notice, changed)
	}
	if _, ok, _ := s.Load("other/claude-opus-4-1"); ok {
		t.Fatal("a mismatching first fingerprint was stored")
	}
}

// A route that answers nothing fails the check rather than storing an
// empty reference.
func TestFingerprintAllFail(t *testing.T) {
	s := FingerprintStore{Dir: t.TempDir()}
	if _, _, err := s.Session(context.Background(), fpModel{fail: true}, "r/m", "m"); err == nil {
		t.Fatal("want an error")
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Fatalf("stored %d files", len(entries))
	}
}
