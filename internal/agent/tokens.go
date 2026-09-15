package agent

import (
	"github.com/xmonader/janetcode/internal/llm"
	tokenest "github.com/xmonader/janetcode/internal/tokens"
)

// estimateTokens approximates the token count of text. The estimator
// itself lives in internal/tokens, so the skills report and anything
// else that prices text quotes the same number this does.
func estimateTokens(s string) int { return tokenest.Estimate(s) }

// perMessageTokens is the framing overhead each message costs.
const perMessageTokens = 4

// messageTokens estimates one message, billing images by area rather
// than by their base64 length.
func messageTokens(m llm.Message) int {
	n := perMessageTokens
	for _, b := range m.Blocks {
		if b.Type == llm.BlockImage {
			n += 1600 // roughly a screenshot
			continue
		}
		n += estimateTokens(b.Text) + estimateTokens(string(b.Input)) + estimateTokens(b.Name)
	}
	return n
}
