package jev

import (
	"context"
	"strings"
	"testing"
)

func TestEvaluateRejectsUnrepresentableQuestions(t *testing.T) {
	srv, hits := fakeServer(t, 200, okBody)
	c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
	for _, tc := range []struct {
		name string
		qs   []Question
	}{
		{"empty id", []Question{{Kind: Noul}}},
		{"duplicate id", []Question{{ID: "same", Kind: Noul}, {ID: "same", Kind: Noul}}},
		{"unknown kind", []Question{{ID: "q", Kind: Kind("other")}}},
		{"choice without options", []Question{{ID: "q", Kind: Choice}}},
		{"score without levels", []Question{{ID: "q", Kind: Score}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Evaluate(context.Background(), "s", tc.qs); err == nil {
				t.Fatal("invalid questions accepted")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("invalid questions reached the server %d times", hits.Load())
	}
}

func TestEvaluateRejectsMissingOrMistypedAnswers(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing", `{"model":"jev-1.13.0","answers":{}}`},
		{"wrong kind", `{"model":"jev-1.13.0","answers":{"caused":{"type":"choice","choice":"patch"},"class":{"type":"choice","choice":"patch"},"severity":{"type":"score","score":1}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := fakeServer(t, 200, tc.body)
			c := &Client{BaseURL: srv.URL, Key: "k", Model: "jev-1.13.0"}
			res, err := c.Evaluate(context.Background(), "s", testQuestions())
			if err == nil {
				t.Fatalf("malformed answers returned result %+v", res)
			}
			if !strings.Contains(err.Error(), "malformed response") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
