package lsp

import (
	"bufio"
	"context"
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

// With more than one server up, a workspace-wide question used to be sent
// to whichever the map happened to yield first, so "symbols search" asked
// a different language's server on each invocation and gave a different
// answer each time.
func TestQueryClientIsStableAcrossCalls(t *testing.T) {
	m := &Manager{}
	m.clients = map[string]*client{"gopls": {}, "pyright": {}, "rust-analyzer": {}}
	first, err := m.queryClient(context.Background(), "")
	if err != nil || first == nil {
		t.Fatalf("c=%v err=%v", first, err)
	}
	for i := 0; i < 50; i++ {
		c, err := m.queryClient(context.Background(), "")
		if err != nil {
			t.Fatal(err)
		}
		if c != first {
			t.Fatal("the same question went to a different server")
		}
	}
	if first != m.clients["gopls"] {
		t.Fatal("the choice should be the first server by name")
	}
}
