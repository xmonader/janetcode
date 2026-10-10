package main

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/eval"
)

// TestSeqRunRefusesWhatItCannotPair: a sequential run needs a baseline to
// pair against and a gate it knows; either missing is a usage error before
// any run is bought.
func TestSeqRunRefusesWhatItCannotPair(t *testing.T) {
	r := &eval.Runner{Runs: 3}
	if _, err := seqRun(context.Background(), r, nil, nil, "pass"); err == nil || !strings.Contains(err.Error(), "--eval-compare") {
		t.Fatalf("no baseline: %v", err)
	}
	if _, err := seqRun(context.Background(), r, nil, &eval.File{Runs: 3}, "cost"); err == nil || !strings.Contains(err.Error(), "unknown sequential gate") {
		t.Fatalf("bad gate: %v", err)
	}
	// No gate is the ordinary run: nothing to pair, nothing refused.
	if res, err := seqRun(context.Background(), r, nil, nil, ""); err != nil || len(res) != 0 {
		t.Fatalf("plain run: %v %v", res, err)
	}
}
