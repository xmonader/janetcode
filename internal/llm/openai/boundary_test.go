package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type brokenBody struct {
	data []byte
	err  error
	done bool
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.done {
		return 0, io.EOF
	}
	b.done = true
	return copy(p, b.data), b.err
}

func (b *brokenBody) Close() error { return nil }

func TestSuccessBodyReadErrorsAreNotDiscarded(t *testing.T) {
	readErr := errors.New("body read failed")
	for _, tc := range []struct {
		name string
		body string
		call func(*Provider) error
	}{
		{"models", `{"data":[]}`, func(p *Provider) error {
			_, err := p.ListModels(context.Background())
			return err
		}},
		{"embeddings", `{"data":[{"index":0,"embedding":[1]}]}`, func(p *Provider) error {
			_, err := p.Embed(context.Background(), "emb", []string{"x"})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: &brokenBody{data: []byte(tc.body), err: readErr}, Header: make(http.Header)}, nil
			})}
			p, err := New(Config{BaseURL: "http://provider.test/v1", APIKeys: []string{"k"}, HTTPClient: hc})
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(p); !errors.Is(err, readErr) {
				t.Fatalf("err = %v, want body read failure", err)
			}
		})
	}
}

func TestEmbedRejectsDuplicateVectorIndices(t *testing.T) {
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := `{"data":[{"index":0,"embedding":[1]},{"index":0,"embedding":[2]}]}`
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	p, err := New(Config{BaseURL: "http://provider.test/v1", APIKeys: []string{"k"}, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	vecs, err := p.Embed(context.Background(), "emb", []string{"a", "b"})
	if err == nil {
		t.Fatalf("duplicate indices returned vectors %v without an error", vecs)
	}
}
