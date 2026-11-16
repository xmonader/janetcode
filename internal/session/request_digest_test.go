package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The streamed digest must equal a digest of json.Marshal over the whole
// slice, so recordings made before the change still verify.
func TestDigestMessagesMatchesWholeSliceMarshal(t *testing.T) {
	cases := [][]llm.Message{
		nil,
		{},
		{llm.TextMessage(llm.RoleUser, "hi <b>&</b>")},
		{llm.TextMessage(llm.RoleUser, "a"), llm.TextMessage(llm.RoleAssistant, "b\n\"c\"")},
	}
	for i, msgs := range cases {
		want := msgs
		if want == nil {
			want = []llm.Message{}
		}
		b, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := DigestMessages(msgs); got != hex.EncodeToString(sum[:]) {
			t.Errorf("case %d: streamed digest %s differs from whole-slice digest", i, got)
		}
	}
}
