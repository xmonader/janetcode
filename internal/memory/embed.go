package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// EmbedFunc turns texts into vectors. When a Store has one, recall ranks
// entries by meaning as well as by words, so "verify the build" finds a
// note about running make test. Nil means lexical recall only.
type EmbedFunc func(ctx context.Context, texts []string) ([][]float32, error)

// SemanticMin is the cosine similarity an entry needs to be recalled by
// meaning alone.
const SemanticMin = 0.45

// embedRetry is how long a failing embeddings endpoint is left alone.
const embedRetry = 5 * time.Minute

type vecEntry struct {
	Hash string    `json:"hash"`
	Vec  []float32 `json:"vec"`
}

// RecallCtx is Recall with semantic ranking when an EmbedFunc is set. It
// falls back to lexical recall on any embedding error.
func (s *Store) RecallCtx(ctx context.Context, text string, max int) []Entry {
	return s.rankCtx(ctx, text, max, 4)
}

// SearchCtx is Search with semantic ranking when available.
func (s *Store) SearchCtx(ctx context.Context, text string, max int) []Entry {
	return s.rankCtx(ctx, text, max, 1)
}

func (s *Store) rankCtx(ctx context.Context, text string, max, min int) []Entry {
	if max <= 0 {
		max = MaxRecall
	}
	lexical := s.rank(text, max, min)
	if s.Embed == nil || len(Words(text)) == 0 {
		return lexical
	}
	semantic, ok := s.semantic(ctx, text, max)
	if !ok {
		return lexical
	}
	seen := map[string]bool{}
	var out []Entry
	for _, e := range semantic {
		seen[e.Scope+"/"+e.Name] = true
		out = append(out, e)
	}
	for _, e := range lexical {
		if !seen[e.Scope+"/"+e.Name] {
			out = append(out, e)
		}
	}
	if len(out) > max {
		out = out[:max]
	}
	return out
}

// semantic ranks entries by cosine similarity to the text. ok is false
// when embeddings were unavailable, so the caller uses words instead.
func (s *Store) semantic(ctx context.Context, text string, max int) ([]Entry, bool) {
	s.embedMu.Lock()
	defer s.embedMu.Unlock()
	if !s.embedDown.IsZero() && time.Since(s.embedDown) < embedRetry {
		return nil, false
	}
	entries := s.Entries()
	if len(entries) == 0 {
		return nil, true
	}
	if s.vecs == nil {
		s.loadVecs()
	}
	// Embed the query together with every entry whose text changed.
	texts := []string{textfmt.TruncateTo(text, 2000)}
	var missing []int
	for i, e := range entries {
		key := e.Scope + "/" + e.Name
		if v, ok := s.vecs[key]; !ok || v.Hash != entryHash(e) {
			missing = append(missing, i)
			texts = append(texts, embedText(e))
		}
	}
	vecs, err := s.Embed(ctx, texts)
	if err != nil || len(vecs) != len(texts) {
		s.embedDown = time.Now()
		return nil, false
	}
	s.embedDown = time.Time{}
	query := vecs[0]
	for j, i := range missing {
		e := entries[i]
		s.vecs[e.Scope+"/"+e.Name] = vecEntry{Hash: entryHash(e), Vec: vecs[j+1]}
	}
	if len(missing) > 0 {
		s.saveVecs()
	}
	type scored struct {
		e   Entry
		sim float64
	}
	var hits []scored
	for _, e := range entries {
		// Meaning alone never recalls a global entry that is hidden here
		// or needs a strong word match (see Store.hidden).
		if hide, strong := s.hidden(e); hide || strong {
			continue
		}
		v := s.vecs[e.Scope+"/"+e.Name]
		if sim := cosine(query, v.Vec); sim >= SemanticMin {
			hits = append(hits, scored{e, sim})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].sim > hits[j].sim })
	if len(hits) > max {
		hits = hits[:max]
	}
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out, true
}

func embedText(e Entry) string {
	return textfmt.TruncateTo(e.Name+": "+e.Description+"\n"+e.Body, 2000)
}

func entryHash(e Entry) string {
	sum := sha256.Sum256([]byte(embedText(e)))
	return hex.EncodeToString(sum[:8])
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// vecPath is where the store caches vectors, next to the entries.
func (s *Store) vecPath() string {
	return filepath.Join(s.Root, "embeddings-"+s.Key+".json")
}

func (s *Store) loadVecs() {
	s.vecs = map[string]vecEntry{}
	data, err := os.ReadFile(s.vecPath())
	if err != nil {
		return
	}
	_ = json.Unmarshal(data, &s.vecs)
}

func (s *Store) saveVecs() {
	data, err := json.Marshal(s.vecs)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.Root, 0o755); err != nil {
		return
	}
	_ = fsutil.AtomicWrite(s.vecPath(), data)
}
