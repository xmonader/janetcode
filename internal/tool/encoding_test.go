package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeEncodeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		want    string
		charset string
		bom     bool
		crlf    bool
	}{
		{"plain", []byte("a\nb\n"), "a\nb\n", "utf-8", false, false},
		{"utf8 bom", []byte("\xef\xbb\xbfa\nb\n"), "a\nb\n", "utf-8", true, false},
		{"crlf", []byte("a\r\nb\r\n"), "a\nb\n", "utf-8", false, true},
		{"utf8 bom crlf", []byte("\xef\xbb\xbfa\r\nb\r\n"), "a\nb\n", "utf-8", true, true},
		{"utf16le", append([]byte{0xff, 0xfe}, encodeUTF16("a\nb\n", false)...), "a\nb\n", "utf-16le", true, false},
		{"utf16be", append([]byte{0xfe, 0xff}, encodeUTF16("a\nb\n", true)...), "a\nb\n", "utf-16be", true, false},
		{"utf16le crlf", append([]byte{0xff, 0xfe}, encodeUTF16("a\r\nb\r\n", false)...), "a\nb\n", "utf-16le", true, true},
		// 0xe9 is é in Latin-1 and not valid UTF-8.
		{"latin-1", []byte("caf\xe9\n"), "café\n", "latin-1", false, false},
	}
	for _, c := range cases {
		text, f := decodeText(c.raw)
		if text != c.want {
			t.Errorf("%s: text = %q, want %q", c.name, text, c.want)
		}
		if f.Charset != c.charset || (f.BOM != "") != c.bom || f.CRLF != c.crlf {
			t.Errorf("%s: form = %+v", c.name, f)
		}
		if back := encodeText(text, f); string(back) != string(c.raw) {
			t.Errorf("%s: re-encoded %q, want %q", c.name, back, c.raw)
		}
	}
}

// An edit must not rewrite every line of a Windows file, nor drop its mark.
func TestEditKeepsBOMAndCRLF(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("\xef\xbb\xbfone\r\ntwo\r\n"), 0o644)
	e := EditFile{Root: root}
	out, err := e.Run(context.Background(), editIn("a.txt", "two", "three"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "CRLF") || !strings.Contains(out, "byte-order mark") {
		t.Fatalf("the result should name the form it preserved: %q", out)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "\xef\xbb\xbfone\r\nthree\r\n" {
		t.Fatalf("file = %q", data)
	}
}

func TestWriteKeepsTheFilesForm(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "a.txt")
	os.WriteFile(p, []byte("\xff\xfe"+string(encodeUTF16("old\r\n", false))), 0o644)
	seen := &Seen{}
	if _, err := (ReadFile{Root: root, Seen: seen}).Run(context.Background(), json.RawMessage(`{"path":"a.txt"}`)); err != nil {
		t.Fatal(err)
	}
	w := WriteFile{Root: root, Seen: seen}
	if _, err := w.Run(context.Background(), json.RawMessage(`{"path":"a.txt","content":"new\n"}`)); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	want := "\xff\xfe" + string(encodeUTF16("new\r\n", false))
	if string(data) != want {
		t.Fatalf("file = %q, want %q", data, want)
	}
	// A new file is plain UTF-8 with LF; nothing is invented.
	if _, err := w.Run(context.Background(), json.RawMessage(`{"path":"b.txt","content":"x\n"}`)); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(data) != "x\n" {
		t.Fatalf("new file = %q", data)
	}
}

func TestReadNamesTheEncoding(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("caf\xe9\n"), 0o644)
	out, err := (ReadFile{Root: root}).Run(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "latin-1") {
		t.Fatalf("the read should name the charset it guessed: %q", out)
	}
	if !strings.Contains(out, "café") {
		t.Fatalf("the byte should have been decoded: %q", out)
	}
}

func TestBinaryIsRefusedAsTextButUTF16IsNot(t *testing.T) {
	if isBinary([]byte("ok\n")) {
		t.Fatal("plain text read as binary")
	}
	if !isBinary([]byte("a\x00b")) {
		t.Fatal("a NUL byte is binary")
	}
	// A NUL past the sniff window does not make the file binary.
	long := append(make([]byte, binarySniffBytes), 0)
	for i := range long[:binarySniffBytes] {
		long[i] = 'a'
	}
	if isBinary(long) {
		t.Fatal("a NUL past the first 4 KB should not count")
	}
	if isBinary(append([]byte{0xff, 0xfe}, encodeUTF16("hi", false)...)) {
		t.Fatal("UTF-16 is text")
	}

	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.bin"), []byte("bin\x00ary\n"), 0o644)
	_, err := (EditFile{Root: root}).Run(context.Background(), editIn("a.bin", "bin", "BIN"))
	var pr *ErrPriorRead
	if err == nil || !asPriorRead(err, &pr) || pr.Cause != CauseBinary {
		t.Fatalf("editing a binary file: %v", err)
	}
}

func asPriorRead(err error, out **ErrPriorRead) bool {
	pr, ok := err.(*ErrPriorRead)
	if ok {
		*out = pr
	}
	return ok
}
