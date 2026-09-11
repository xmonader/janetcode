package session

import (
	"testing"
)

// BenchmarkAppendEvent measures what one logged event costs. Every tool
// call, result, notice and verdict of a turn goes through here.
func BenchmarkAppendEvent(b *testing.B) {
	dir := b.TempDir()
	s, err := New(dir, dir, "openai", "m")
	if err != nil {
		b.Fatal(err)
	}
	ev := Event{Type: "tool_result", Output: "internal/server/handler.go\ninternal/server/routes.go\n"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Append(ev); err != nil {
			b.Fatal(err)
		}
	}
}
