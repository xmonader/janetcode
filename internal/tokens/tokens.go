// Package tokens holds the one token estimator janetcode uses. It is a
// leaf package (standard library only) so that the agent's context
// accounting and anything else that has to price a piece of text — the
// skills report, a budget check — agree to the token instead of each
// carrying its own bytes-over-four guess.
package tokens

// Estimate approximates the token count of text by character class:
// letters run about four per token, digits and symbol-heavy code less,
// non-ASCII text (and base64) far less. Within about 20% for prose and
// code; a flat bytes/4 is off by 40% on code.
func Estimate(s string) int {
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
