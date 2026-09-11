package lsp

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadMessage(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1}`
	r := bufio.NewReader(strings.NewReader("Content-Length: 24\r\n\r\n" + body))
	got, err := readMessage(r)
	if err != nil || string(got) != body {
		t.Fatalf("got %q err=%v", got, err)
	}
}

// The length is whatever the server said, and the allocation it asks for
// is a fatal runtime error no recover can catch. A server that crashes
// mid-write, or puts something that is not LSP on stdout, must get an
// error rather than take janetcode down with it.
func TestReadMessageRejectsAbsurdLength(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("Content-Length: 9223372036854775807\r\n\r\n"))
	if _, err := readMessage(r); err == nil {
		t.Fatal("a frame larger than any machine's memory was accepted")
	}
	r = bufio.NewReader(strings.NewReader("Content-Length: -1\r\n\r\n"))
	if _, err := readMessage(r); err == nil {
		t.Fatal("a negative length was accepted")
	}
}
