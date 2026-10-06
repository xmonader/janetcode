package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/llm"
)

type listerProv struct {
	models []llm.ModelInfo
	err    error
}

func (listerProv) Name() string { return "fake" }
func (listerProv) Stream(context.Context, llm.Request, func(llm.Event)) (*llm.Response, error) {
	return nil, errors.New("not used")
}
func (p listerProv) ListModels(context.Context) ([]llm.ModelInfo, error) { return p.models, p.err }

func TestListModels(t *testing.T) {
	p := listerProv{models: []llm.ModelInfo{{ID: "a-1", ContextWindow: 200000, MaxOutput: 64000}, {ID: "b-2", Name: "B", ContextWindow: 131072}}}
	var out bytes.Buffer
	if err := listModels(context.Background(), &out, p, headless.FormatText); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "a-1") || !strings.Contains(out.String(), "200k") || !strings.Contains(out.String(), "131.1k") {
		t.Fatalf("text = %s", out.String())
	}
	out.Reset()
	if err := listModels(context.Background(), &out, p, headless.FormatJSON); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if json.Unmarshal(out.Bytes(), &rows) != nil || len(rows) != 2 || rows[1]["name"] != "B" || rows[0]["context_window"] != 200000.0 {
		t.Fatalf("json = %s", out.String())
	}
	if err := listModels(context.Background(), &out, nil, headless.FormatText); !errors.Is(err, llm.ErrNoAPIKey) {
		t.Fatalf("nil provider: %v", err)
	}
	if err := listModels(context.Background(), &out, listerProv{err: errors.New("401")}, headless.FormatText); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("list error: %v", err)
	}
}
