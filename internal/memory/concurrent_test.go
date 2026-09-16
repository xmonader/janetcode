package memory

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// /cd re-points the store at another project while a turn is still
// running: Rekey writes the project key, the ecosystem and the vector
// cache, and recall reads all three. Run under -race this catches a
// reader touching one of them outside the lock.
func TestRekeyRacesRecall(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	s := Open(root, a)
	s.Embed = func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{float32(len(texts[i])), 1, 0}
		}
		return out, nil
	}
	if _, _, err := s.Put(Entry{Description: "the build runs with go test ./...", Type: TypeProject}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Put(Entry{Description: "prefer gofmt over manual alignment", Scope: "global", Source: "user"}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 20; n++ {
				s.RecallCtx(context.Background(), "how does the build run with go test", 3)
				s.Search("gofmt alignment", 3)
				s.IndexWithin(IndexBudget)
				_ = s.ProjectKey()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for n := 0; n < 20; n++ {
			s.Rekey(a)
			s.Rekey(b)
		}
	}()
	wg.Wait()
}
