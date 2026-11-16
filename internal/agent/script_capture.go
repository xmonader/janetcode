package agent

import (
	"fmt"
	"strings"
)

// scriptCapture bounds pipe memory while retaining the beginning and end of
// noisy scripts. Write accepts all bytes so truncation cannot break the child.
type scriptCapture struct {
	limit int
	head  []byte
	tail  []byte
	total int64
}

func (b *scriptCapture) Write(p []byte) (int, error) {
	n := len(p)
	b.total += int64(n)
	headLimit := b.limit * 2 / 3
	if room := headLimit - len(b.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.head = append(b.head, p[:room]...)
		p = p[room:]
	}
	tailLimit := b.limit - headLimit
	if len(p) >= tailLimit {
		b.tail = append(b.tail[:0], p[len(p)-tailLimit:]...)
	} else {
		if extra := len(b.tail) + len(p) - tailLimit; extra > 0 {
			copy(b.tail, b.tail[extra:])
			b.tail = b.tail[:len(b.tail)-extra]
		}
		b.tail = append(b.tail, p...)
	}
	return n, nil
}

func (b *scriptCapture) String() string {
	if b.total <= int64(b.limit) {
		return string(b.head) + string(b.tail)
	}
	// Discard partial runes at retained edges and malformed subprocess bytes.
	head := strings.ToValidUTF8(string(b.head), "")
	tail := strings.ToValidUTF8(string(b.tail), "")
	dropped := b.total - int64(len(head)+len(tail))
	return head + fmt.Sprintf("\n\n... [%d bytes of output dropped; print less] ...\n\n", dropped) + tail
}
