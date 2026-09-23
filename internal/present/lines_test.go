package present

import "testing"

func TestVerdictTone(t *testing.T) {
	for text, want := range map[string]Tone{
		"✓ verified: 1 file edited": ToneOK,
		"✗ tests fail: 3 failed":    ToneBad,
		"⚠ unverified: nothing ran": ToneWarn,
		"· no changes: docs only":   ToneWarn,
	} {
		if got := VerdictTone(text); got != want {
			t.Errorf("%q = %s, want %s", text, got, want)
		}
	}
}
