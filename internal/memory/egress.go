package memory

import (
	"context"
	"fmt"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
)

// noteCall records in the egress ledger on ctx (if any) that a memory
// model call is about to send the conversation, or the session's record,
// to the provider: what it is, how many messages, and the size of the
// text. The provider's host and byte count come from the HTTP transport;
// this entry says why the request exists. Recorder.Add scrubs it.
func noteCall(ctx context.Context, what, system string, msgs []llm.Message) {
	n := int64(len(system))
	for _, m := range msgs {
		n += int64(len(m.Text()))
	}
	egress.From(ctx).Add(egress.Entry{Kind: egress.KindMemoryCall, What: what, Bytes: n})
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	switch noun {
	case "existing entry":
		return fmt.Sprintf("%d existing entries", n)
	case "memory entry":
		return fmt.Sprintf("%d memory entries", n)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
