package lsp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// maxFrameBytes bounds one LSP message. Real ones are a few hundred
// kilobytes at the very most: a whole-file didOpen or a project-wide
// symbol list.
const maxFrameBytes = 64 << 20

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, errors.New("lsp: message without Content-Length")
	}
	// The length is the server's word for how much to allocate. A language
	// server that crashes mid-write, or writes something that is not LSP
	// at all onto stdout, can name a number no machine has the memory for
	// — and an allocation that big is a fatal runtime error that no
	// recover can catch, so it takes janetcode with it. A frame larger
	// than this is not a frame.
	if length > maxFrameBytes {
		return nil, fmt.Errorf("lsp: message of %d bytes exceeds the %d-byte limit", length, maxFrameBytes)
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	_, err := l.w.Write(p)
	return len(p), err
}
