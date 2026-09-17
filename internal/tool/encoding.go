package tool

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// A file on disk is bytes, and the model works in UTF-8 text with LF line
// endings. Everything that is lost in between — the byte-order mark, CRLF,
// a file that is not UTF-8 at all — has to come back when the file is
// written, or the agent silently rewrites every line of a Windows repo or
// turns a Latin-1 file into mojibake on its first edit.
//
// form is what a file was, kept so it can be put back.
type form struct {
	// BOM is the byte-order mark the file began with, verbatim.
	BOM string
	// CRLF means the file used \r\n line endings.
	CRLF bool
	// Charset is "utf-8", "utf-16le", "utf-16be" or "latin-1".
	Charset string
}

// Plain reports whether the file needs nothing put back.
func (f form) Plain() bool { return f.BOM == "" && !f.CRLF && f.Charset == "utf-8" }

// Note describes the form for a tool result, or "" when there is nothing
// worth saying.
func (f form) Note() string {
	if f.Plain() {
		return ""
	}
	var parts []string
	if f.Charset != "utf-8" {
		parts = append(parts, f.Charset)
	}
	if f.BOM != "" {
		parts = append(parts, "a byte-order mark")
	}
	if f.CRLF {
		parts = append(parts, "CRLF line endings")
	}
	return "this file is " + strings.Join(parts, " with ") + "; it is shown as UTF-8 with LF and written back in its own form"
}

const (
	bomUTF8   = "\xef\xbb\xbf"
	bomUTF16L = "\xff\xfe"
	bomUTF16B = "\xfe\xff"
)

// decodeText turns a file's bytes into the UTF-8, LF text the model works
// in, and reports what has to be restored when it is written back.
func decodeText(data []byte) (string, form) {
	f := form{Charset: "utf-8"}
	s := string(data)
	switch {
	case strings.HasPrefix(s, bomUTF8):
		f.BOM, s = bomUTF8, s[len(bomUTF8):]
	case strings.HasPrefix(s, bomUTF16L):
		f.BOM, f.Charset = bomUTF16L, "utf-16le"
		s = decodeUTF16(data[2:], false)
	case strings.HasPrefix(s, bomUTF16B):
		f.BOM, f.Charset = bomUTF16B, "utf-16be"
		s = decodeUTF16(data[2:], true)
	case !utf8.ValidString(s):
		// No mark and not UTF-8. There is no way to know the charset for
		// certain, and guessing wrong is worse than a readable guess that
		// round-trips: Latin-1 maps every byte to a character and back,
		// so an edit to one part of the file cannot corrupt the rest.
		f.Charset = "latin-1"
		s = decodeLatin1(data)
	}
	if strings.Contains(s, "\r\n") {
		f.CRLF = true
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	return s, f
}

// encodeText is decodeText's inverse: the model's UTF-8 LF text written
// back the way the file was.
// encodeChecked encodes s in the file's shape and refuses the result when
// the bytes that would land on disk exceed the write ceiling.
//
// The ceiling was measured against the text, which is UTF-8, while the
// write is in the file's own charset. A UTF-16 file spends two bytes on
// nearly every character, so content that passed the 8 MiB check wrote
// 16 MiB — the one cap that is supposed to stand between a runaway model
// and the disk, overshot by the factor the file's encoding happens to be.
func encodeChecked(s string, f form, what, hint string) ([]byte, error) {
	data := encodeText(s, f)
	if err := checkWriteSize(what, len(data), hint); err != nil {
		return nil, err
	}
	return data, nil
}

func encodeText(s string, f form) []byte {
	if f.CRLF {
		// Any \r\n the text already has must not become \r\r\n.
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	}
	switch f.Charset {
	case "utf-16le":
		return append([]byte(f.BOM), encodeUTF16(s, false)...)
	case "utf-16be":
		return append([]byte(f.BOM), encodeUTF16(s, true)...)
	case "latin-1":
		return append([]byte(f.BOM), encodeLatin1(s)...)
	}
	return append([]byte(f.BOM), s...)
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(u))
}

func encodeUTF16(s string, bigEndian bool) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u)*2)
	for _, c := range u {
		if bigEndian {
			out = append(out, byte(c>>8), byte(c))
		} else {
			out = append(out, byte(c), byte(c>>8))
		}
	}
	return out
}

func decodeLatin1(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func encodeLatin1(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r < 0x100 {
			out = append(out, byte(r))
			continue
		}
		// The model wrote a character Latin-1 cannot hold. Encoding it as
		// UTF-8 keeps the character rather than replacing it with a ?,
		// which is the lesser of the two damages.
		out = utf8.AppendRune(out, r)
	}
	return out
}

// binarySniffBytes is how much of a file is examined for NUL bytes. A text
// file does not contain one; a compiled object or an image does, usually
// in its header.
const binarySniffBytes = 4096
