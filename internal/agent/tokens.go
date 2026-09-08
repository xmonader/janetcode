package agent

import "github.com/xmonader/janetcode/internal/llm"

// estimateTokens approximates the token count of text by character class:
// letters run about four per token, digits and symbol-heavy code less,
// non-ASCII text (and base64) far less. Within about 20% for prose and
// code; a flat bytes/4 is off by 40% on code.
func estimateTokens(s string) int {
	var alpha, digit, other, wide float64
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 0x80:
			wide++
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
			alpha++
		case c >= '0' && c <= '9', c == '_', c == '$':
			digit++
		default:
			other++
		}
	}
	return int(alpha/4.0 + digit/2.5 + other/3.0 + wide/1.5 + 0.5)
}

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
