package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

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

// An image is billed by what it costs the model, not by the length of
// the base64 that carries it: a screenshot is about 1600 tokens, while
// its encoding is hundreds of kilobytes, and estimating from the string
// would put a session over the compaction threshold on one paste.
func TestImagesAreEstimatedByAreaNotByTheirEncoding(t *testing.T) {
	data := strings.Repeat("A", 400_000)
	m := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockImage, MediaType: "image/png", Data: data},
	}}
	got := messageTokens(m)
	if got != perMessageTokens+1600 {
		t.Fatalf("image message = %d tokens, want %d", got, perMessageTokens+1600)
	}
	if got >= estimateTokens(data) {
		t.Fatal("the image was estimated from its encoding")
	}
	two := llm.Message{Role: llm.RoleUser, Blocks: append(m.Blocks, m.Blocks[0], llm.Block{Type: llm.BlockText, Text: "what is this?"})}
	if messageTokens(two) < 2*1600 {
		t.Fatalf("two images = %d tokens", messageTokens(two))
	}
}
