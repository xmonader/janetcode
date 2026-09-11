package rates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkLoadRealTable measures what the cached LiteLLM table costs at
// startup. Point JANETCODE_RATES_FILE at a real one (~2.3 MB, about
// 1,800 models); without it the benchmark is skipped rather than
// measuring a toy.
func BenchmarkLoadRealTable(b *testing.B) {
	p := os.Getenv("JANETCODE_RATES_FILE")
	if p == "" {
		b.Skip("set JANETCODE_RATES_FILE to a cached model-rates.json")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		b.Skip(err)
	}
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "model-rates.json"), data, 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if Load(dir) == nil {
			b.Fatal("no table")
		}
	}
}

// BenchmarkParseVariants splits the cost of Load into reading the file
// and parsing it, and compares the two-pass parse with a direct one.
func BenchmarkParseVariants(b *testing.B) {
	p := os.Getenv("JANETCODE_RATES_FILE")
	if p == "" {
		b.Skip("set JANETCODE_RATES_FILE to a cached model-rates.json")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		b.Skip(err)
	}
	b.Run("read", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = os.ReadFile(p)
		}
	})
	b.Run("current", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := Parse(data); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("direct", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var m map[string]Entry
			if err := json.Unmarshal(data, &m); err != nil {
				b.Fatal(err)
			}
			if len(m) == 0 {
				b.Fatal("empty")
			}
		}
	})
}
