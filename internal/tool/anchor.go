package tool

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/textfmt"
)

// Content-hash line anchors.
//
// A line number says where a line was when it was read. If anything above
// it changes — the model's own earlier edit in the same batch, a file
// rewritten by a build — the number now points somewhere else, and an edit
// made against it lands on the wrong line and reports success.
//
// An anchor is a line number and a short hash of the line's content:
// "12:abc". The number finds the line quickly, and the hash says whether
// it is still the line that was read. When it is not, the same content is
// looked for nearby, and the edit follows the line it was aimed at rather
// than the position it used to occupy. When it cannot be found, or could
// be one of several, the edit is refused instead of guessed.
//
// The hash ignores indentation and collapses internal whitespace, so
// reformatting does not invalidate every anchor in a file, while any real
// change to the line does.

// anchorRadius is how far from the recorded line number to look for a
// line that has moved. Far enough for edits above it, near enough that a
// coincidentally identical line elsewhere in the file is not a candidate.
const anchorRadius = 40

// lineAnchor is the three-letter content hash of one line.
func lineAnchor(line string) string {
	// FNV-1a over the normalized line.
	const offset64, prime64 = 14695981039346656037, 1099511628211
	h := uint64(offset64)
	for _, r := range normalizeForAnchor(line) {
		h ^= uint64(r)
		h *= prime64
	}
	const letters = "abcdefghijklmnopqrstuvwxyz"
	// Three letters drawn from different parts of the hash, so lines that
	// differ late are still distinguished.
	return string([]byte{
		letters[h%26],
		letters[(h>>21)%26],
		letters[(h>>42)%26],
	})
}

// normalizeForAnchor makes the hash survive reformatting but not a change
// of content: leading and trailing space is dropped and runs of internal
// whitespace become one space.
func normalizeForAnchor(line string) string {
	return strings.Join(strings.Fields(line), " ")
}

// Anchor renders the anchor for a line at a 1-based number.
func Anchor(n int, line string) string { return fmt.Sprintf("%d:%s", n, lineAnchor(line)) }

// anchorRef is a parsed "12:abc".
type anchorRef struct {
	line int
	hash string
}

// parseAnchor reads "12:abc". A bare number is accepted and means the
// caller is using a line number, with no claim about content.
func parseAnchor(s string) (anchorRef, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return anchorRef{}, fmt.Errorf("empty anchor")
	}
	num, hash, found := strings.Cut(s, ":")
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if err != nil || n < 1 {
		return anchorRef{}, fmt.Errorf("anchor %q should look like 12:abc, a line number and the three letters read_file showed beside it", s)
	}
	hash = strings.TrimSpace(hash)
	if found && len(hash) != 3 {
		return anchorRef{}, fmt.Errorf("anchor %q should look like 12:abc; %q is not a three-letter mark", s, hash)
	}
	return anchorRef{line: n, hash: hash}, nil
}

// resolveAnchor finds the 1-based line an anchor refers to.
//
// The recorded position is tried first, then the neighbourhood. A line
// that has moved is followed only when exactly one candidate nearby
// matches: two identical lines are not a location, and choosing between
// them would be a guess that silently edits the wrong one.
func resolveAnchor(ref anchorRef, lines []string) (int, error) {
	if len(lines) == 0 {
		return 0, fmt.Errorf("the file is empty")
	}
	at := func(n int) string {
		if n < 1 || n > len(lines) {
			return ""
		}
		return lines[n-1]
	}
	if ref.hash == "" {
		if ref.line > len(lines) {
			return 0, fmt.Errorf("line %d is past the end of the file, which has %d line(s)", ref.line, len(lines))
		}
		return ref.line, nil
	}
	if l := at(ref.line); l != "" && lineAnchor(l) == ref.hash {
		return ref.line, nil
	}
	var found []int
	for d := 1; d <= anchorRadius; d++ {
		for _, n := range [2]int{ref.line - d, ref.line + d} {
			if l := at(n); l != "" && lineAnchor(l) == ref.hash {
				found = append(found, n)
			}
		}
		// Stop at the first distance that matches, so the nearest line
		// wins and a far-off duplicate does not make it ambiguous.
		if len(found) > 0 {
			break
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return 0, fmt.Errorf("anchor %d:%s no longer matches: line %d now reads %q. Read the file again to get current anchors",
			ref.line, ref.hash, ref.line, trimForMessage(at(ref.line)))
	default:
		return 0, fmt.Errorf("anchor %d:%s matches lines %d and %d equally; read the file again and use the line you mean",
			ref.line, ref.hash, found[0], found[1])
	}
}

func trimForMessage(s string) string {
	return textfmt.ClipBytes(strings.TrimSpace(s), 60)
}
