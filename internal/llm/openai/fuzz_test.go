package openai

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// FuzzReadStream feeds arbitrary SSE bodies to the stream assembler. A
// provider — or anything between us and it — can send any bytes at all,
// and a malformed frame has to come back as an error, never as a panic
// that takes the session down mid-turn.
func FuzzReadStream(f *testing.F) {
	f.Add("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":-9223372036854775808,\"function\":{\"arguments\":\"{\"}}]}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {\"usage\":{\"prompt_tokens\":-5,\"prompt_tokens_details\":{\"cached_tokens\":99999999}}}\n\ndata: [DONE]\n\n")
	f.Add("data:\n\ndata: []\n\ndata: [DONE]\n\n")
	// Two index-less calls opened together and continued by id, and a
	// provider that resends the function name on every delta: the two
	// assembly shapes that used to glue calls or names together.
	f.Add("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"a\",\"function\":{\"name\":\"read_file\"}},{\"id\":\"b\",\"function\":{\"name\":\"grep\"}}]}}]}\n\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"a\",\"function\":{\"arguments\":\"{}\"}},{\"id\":\"b\",\"function\":{\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n")
	f.Add("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\"}}]}}]}\n\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\ndata: [DONE]\n\n")
	f.Fuzz(func(t *testing.T, body string) {
		_, _ = readStream(strings.NewReader(body), nil, func(llm.Event) {})
	})
}
