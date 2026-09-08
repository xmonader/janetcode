package agent

import "testing"

func TestEstimateTokensByCharacterClass(t *testing.T) {
	prose := "The quick brown fox jumps over the lazy dog and keeps running through the forest until night."
	code := "func (a *Agent) call(ctx context.Context) (*llm.Response, error) {\n\tif a.x == nil { return nil, errors.New(\"no\") }\n}"
	if got, flat := estimateTokens(prose), len(prose)/4; got < flat*9/10 || got > flat*12/10 {
		t.Fatalf("prose: %d vs flat %d", got, flat)
	}
	// Code has more punctuation per byte, so more tokens than bytes/4.
	if got, flat := estimateTokens(code), len(code)/4; got <= flat {
		t.Fatalf("code: %d should exceed flat %d", got, flat)
	}
	b64 := "aGVsbG8gd29ybGQgdGhpcyBpcyBiYXNlNjQgdGV4dCB0aGF0IGlzIGxvbmc="
	if got := estimateTokens(b64); got < len(b64)/4 {
		t.Fatalf("base64: %d < flat %d", got, len(b64)/4)
	}
	if estimateTokens("") != 0 {
		t.Fatal("empty string should be 0")
	}
}
