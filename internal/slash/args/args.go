// Package args tokenizes the argument text of a slash command into
// positional arguments, flags and key=value pairs. It imports only the
// standard library, so any package that parses a command's arguments can
// use it without pulling in the command table.
package args

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Spec describes the flags one command's argument text may carry, for
// Tokenize. Every flag is matched as a whole whitespace-separated token,
// so a flag's name inside a longer word ("fix--checker") is not a flag.
type Spec struct {
	// Bool flags take no value: "--dry-run".
	Bool []string
	// Value flags take the next token as their value: "--turn 4".
	Value []string
	// Rest flags take the rest of the line, verbatim but trimmed, as
	// their value: "--check go test ./...". Nothing after one is
	// tokenized.
	Rest []string
	// KV collects "key=value" tokens into Args.KV instead of Positional.
	KV bool
}

// Args is argument text split by Tokenize.
type Args struct {
	// Positional is every token that is not a flag, flag value, or
	// (with Spec.KV) key=value pair, in order.
	Positional []string
	// Flags maps each flag present to its value ("" for a Bool flag, or
	// a Rest flag with nothing after it). A later repeat wins.
	Flags map[string]string
	// KV maps each key=value token's key to its value. A later repeat
	// wins. Nil unless Spec.KV.
	KV map[string]string
}

// Has reports whether flag was given.
func (a Args) Has(flag string) bool {
	_, ok := a.Flags[flag]
	return ok
}

// MissingValueError is a Value flag with no value after it (the end of
// the line, or a Rest flag, came next).
type MissingValueError struct{ Flag string }

func (e *MissingValueError) Error() string { return e.Flag + " needs a value" }

// EmptyKeyError is a key=value token with nothing before the "=".
type EmptyKeyError struct{ Token string }

func (e *EmptyKeyError) Error() string { return fmt.Sprintf("bad parameter %q", e.Token) }

// Tokenize splits a command's argument text into positional arguments,
// flags and key=value pairs as spec describes. It is the one tokenizer
// the per-command parsers (/eval keep, /recipe, /undo, /proof, /accept)
// build on, so a flag means the same thing wherever it appears.
func Tokenize(arg string, spec Spec) (Args, error) {
	out := Args{Flags: map[string]string{}}
	if spec.KV {
		out.KV = map[string]string{}
	}
	for rest := arg; ; {
		tok, after := nextToken(rest)
		if tok == "" {
			return out, nil
		}
		rest = after
		switch {
		case slices.Contains(spec.Rest, tok):
			out.Flags[tok] = strings.TrimSpace(rest)
			return out, nil
		case slices.Contains(spec.Bool, tok):
			out.Flags[tok] = ""
		case slices.Contains(spec.Value, tok):
			val, after := nextToken(rest)
			if val == "" || slices.Contains(spec.Rest, val) {
				return Args{}, &MissingValueError{Flag: tok}
			}
			out.Flags[tok] = val
			rest = after
		case spec.KV && strings.Contains(tok, "="):
			k, v, _ := strings.Cut(tok, "=")
			if k == "" {
				return Args{}, &EmptyKeyError{Token: tok}
			}
			out.KV[k] = v
		default:
			out.Positional = append(out.Positional, tok)
		}
	}
}

// CutWord splits s into its first whitespace-separated word and the text
// after it (untrimmed), for a command whose first word is a verb or a
// name rather than something Tokenize should interpret.
func CutWord(s string) (word, rest string) {
	return nextToken(s)
}

// nextToken returns the first whitespace-separated token of s and the
// text after it; tok is "" when s holds only whitespace.
func nextToken(s string) (tok, rest string) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	end := strings.IndexFunc(s, unicode.IsSpace)
	if end < 0 {
		return s, ""
	}
	return s[:end], s[end:]
}
