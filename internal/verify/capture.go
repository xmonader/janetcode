package verify

import (
	"bytes"
	"fmt"
)

// maxEnd is how much of each end of a command's output is kept. A check
// command is a build or a test suite and can print for as long as it
// runs; CombinedOutput held all of it, so a verbose failing suite was an
// unbounded allocation on the machine. What the model is shown is clipped
// to a few thousand bytes of each end anyway (Result.Feedback), so this
// keeps far more than is ever read and no longer grows without limit.
const maxEnd = 512 << 10

// capture is an io.Writer that keeps the first maxEnd bytes and the last
// maxEnd bytes of what is written to it, counting what fell out between
// them. It is not safe for concurrent use; one capture takes both a
// command's stdout and its stderr, which os/exec serialises by handing
// them the same pipe.
type capture struct {
	head    bytes.Buffer
	tail    []byte
	dropped int64
}

func (c *capture) Write(p []byte) (int, error) {
	n := len(p)
	if room := maxEnd - c.head.Len(); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.head.Write(p[:room])
		p = p[room:]
	}
	if len(p) == 0 {
		return n, nil
	}
	c.tail = append(c.tail, p...)
	if over := len(c.tail) - maxEnd; over > 0 {
		c.tail = append(c.tail[:0], c.tail[over:]...)
		c.dropped += int64(over)
	}
	return n, nil
}

func (c *capture) String() string {
	if c.dropped == 0 {
		return c.head.String() + string(c.tail)
	}
	return fmt.Sprintf("%s\n... [%d bytes omitted: the command printed more than is kept] ...\n%s",
		c.head.String(), c.dropped, c.tail)
}
