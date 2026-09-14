package memory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// assertTrunc checks that result is valid UTF-8, fits within max bytes,
// and has no truncated multi-byte character before a trailing ellipsis.
func assertTrunc(t *testing.T, label, result string, max int) {
	t.Helper()
	if !utf8.ValidString(result) {
		t.Errorf("%s: invalid UTF-8: %q", label, result)
	}
	if len(result) > max {
		t.Errorf("%s: length %d exceeds max %d: %q", label, len(result), max, result)
	}
	if strings.HasSuffix(result, "…") && len(result) > len("…") {
		prefix := result[:len(result)-len("…")]
		if r, size := utf8.DecodeLastRuneInString(prefix); r == utf8.RuneError && size <= 1 {
			t.Errorf("%s: truncated UTF-8 before ellipsis: %q", label, result)
		}
	}
}

func TestOneLineUTF8Boundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
	}{
		// 2-byte UTF-8 characters (é = 0xC3 0xA9)
		{"2byte_at_boundary", strings.Repeat("a", 9) + "é", 10},
		{"2byte_split_at_max", strings.Repeat("a", 10) + "é", 10},
		{"2byte_multiple", strings.Repeat("é", 20), 15},

		// 3-byte UTF-8 characters (中 = 0xE4 0xB8 0xAD)
		{"3byte_at_boundary", strings.Repeat("a", 7) + "中", 10},
		{"3byte_split_at_max", strings.Repeat("a", 8) + "中", 10},
		{"3byte_multiple", strings.Repeat("中", 10), 15},

		// 4-byte UTF-8 characters (🎉 = 0xF0 0x9F 0x8E 0x89)
		{"4byte_at_boundary", strings.Repeat("a", 6) + "🎉", 10},
		{"4byte_split_at_max", strings.Repeat("a", 7) + "🎉", 10},
		{"4byte_multiple", strings.Repeat("🎉", 5), 15},

		// Mixed byte lengths
		{"mixed_2_3_4", "aé中🎉" + strings.Repeat("x", 20), 10},
		{"mixed_long", strings.Repeat("a", 5) + "é中🎉" + strings.Repeat("b", 10), 20},

		// Edge cases
		{"all_multibyte", strings.Repeat("é中🎉", 10), 20},
		{"max_equals_len", strings.Repeat("é", 5), 10},
		{"max_one_less", strings.Repeat("é", 5), 9},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertTrunc(t, "oneLine("+tt.name+")", oneLine(tt.in, tt.max), tt.max)
		})
	}
}

func TestFirstLineUTF8Boundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"2byte_multiline", strings.Repeat("é", 200)},
		{"3byte_multiline", strings.Repeat("中", 200)},
		{"4byte_multiline", strings.Repeat("🎉", 200)},
		{"mixed_multiline", strings.Repeat("aé中🎉", 50)},
		{"with_newline", strings.Repeat("é", 100) + "\n" + strings.Repeat("中", 100)},
		{"markdown_heading", "# " + strings.Repeat("🎉", 100)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertTrunc(t, "firstLine("+tt.name+")", firstLine(tt.in), MaxIndexLine)
		})
	}
}

func TestCleanUTF8Boundary(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"2byte_long", strings.Repeat("é", 200)},
		{"3byte_long", strings.Repeat("中", 200)},
		{"4byte_long", strings.Repeat("🎉", 200)},
		{"mixed_long", strings.Repeat("aé中🎉", 50)},
		{"with_spaces", strings.Repeat("hello é 中 🎉 ", 20)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertTrunc(t, "Clean("+tt.name+")", Clean(tt.in), MaxIndexLine)
		})
	}
}
