package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// withEyes gives a the vision slot "eyes", served by vision.
func withEyes(a *Agent, vision llm.Provider) {
	a.Route = func(name string) (llm.Provider, string, error) {
		if name == "eyes" {
			return vision, "eyes-v1", nil
		}
		return nil, "", errors.New("unknown model " + name)
	}
	a.SetVisionModel("eyes")
}

func screenshot(data string) []llm.Block {
	return []llm.Block{
		{Type: llm.BlockText, Text: "why does the build fail?"},
		{Type: llm.BlockImage, MediaType: "image/png", Data: data},
	}
}

func imagesIn(req llm.Request) int {
	n := 0
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockImage {
				n++
			}
		}
	}
	return n
}

func TestAnImageReachesATextOnlyModelAsTheVisionModelsDescription(t *testing.T) {
	main := &fake{responses: []*llm.Response{text("a missing import"), text("fixed")}}
	vision := &fake{responses: []*llm.Response{text("A terminal: `main.go:3: undefined: fmt`")}}
	a, _ := newAgent(t, main, nil)
	withEyes(a, vision)
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}
	if err := a.RunBlocks(context.Background(), screenshot("iVBORw0KGgo="), emit); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "and now?", emit); err != nil {
		t.Fatal(err)
	}
	if len(vision.requests) != 1 {
		t.Fatalf("vision model called %d times, want once for one image", len(vision.requests))
	}
	vr := vision.requests[0]
	if vr.Model != "eyes-v1" || imagesIn(vr) != 1 || !strings.Contains(vr.Messages[0].Text(), "why does the build fail?") {
		t.Fatalf("vision request = %+v", vr)
	}
	for i, req := range main.requests {
		if imagesIn(req) != 0 {
			t.Fatalf("request %d carried an image to the text-only model", i)
		}
		if got := req.Messages[0].Text(); !strings.Contains(got, "undefined: fmt") || !strings.Contains(got, "eyes-v1 described it") {
			t.Fatalf("request %d first message = %q", i, got)
		}
	}
	// The history keeps the image: a switch to a model that sees gets it.
	if a.Messages[0].Blocks[1].Type != llm.BlockImage {
		t.Fatalf("history lost the image: %+v", a.Messages[0].Blocks)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "eyes-v1 described an image") {
		t.Fatalf("notices = %q", notices)
	}
}

func TestAnImageNothingCouldDescribeIsNeverSentRaw(t *testing.T) {
	main := &fake{responses: []*llm.Response{text("ok")}}
	vision := &fake{errs: []error{errors.New("503 service unavailable")}}
	a, _ := newAgent(t, main, nil)
	withEyes(a, vision)
	var notices []string
	if err := a.RunBlocks(context.Background(), screenshot("AAAA"), func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	req := main.requests[0]
	if imagesIn(req) != 0 || !strings.Contains(req.Messages[0].Text(), "could not describe it") {
		t.Fatalf("request = %+v", req.Messages[0])
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "could not describe an image") {
		t.Fatalf("notices = %q", notices)
	}
}

func TestImagesPassThroughWhenTheSessionModelIsTheVisionModel(t *testing.T) {
	main := &fake{responses: []*llm.Response{text("ok")}}
	vision := &fake{}
	a, _ := newAgent(t, main, nil)
	withEyes(a, vision)
	a.SetVisionModel(a.Settings().Model)
	if err := a.RunBlocks(context.Background(), screenshot("AAAA"), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if imagesIn(main.requests[0]) != 1 || len(vision.requests) != 0 {
		t.Fatalf("images to main = %d, vision calls = %d", imagesIn(main.requests[0]), len(vision.requests))
	}
}

func TestAnUnroutableVisionModelLeavesImagesAloneAndSaysSo(t *testing.T) {
	main := &fake{responses: []*llm.Response{text("ok")}}
	a, _ := newAgent(t, main, nil)
	withEyes(a, &fake{})
	a.SetVisionModel("nonesuch")
	var notices []string
	if err := a.RunBlocks(context.Background(), screenshot("AAAA"), func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if imagesIn(main.requests[0]) != 1 || len(notices) != 1 || !strings.Contains(notices[0], "cannot route to nonesuch") {
		t.Fatalf("images = %d, notices = %q", imagesIn(main.requests[0]), notices)
	}
}

// The description cache is bounded, but not below what one request
// carries. It used to be emptied whenever it reached maxDescribed, so a
// history holding more distinct images than that re-described nearly all
// of them on every model call — one vision call per image per request,
// for the rest of the session.
func TestAHistoryWithMoreImagesThanTheCacheBoundIsDescribedOnce(t *testing.T) {
	n := maxDescribed + 1
	replies := make([]*llm.Response, 0, 2*n)
	for range 2 * n {
		replies = append(replies, text("a picture"))
	}
	vision := &fake{responses: replies}
	a, _ := newAgent(t, &fake{}, nil)
	withEyes(a, vision)
	msgs := make([]llm.Message, 0, n)
	for i := range n {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser, Blocks: screenshot(fmt.Sprintf("img-%d", i))})
	}
	a.bridgeImages(context.Background(), msgs, "m", nil)
	if len(vision.requests) != n {
		t.Fatalf("first request described %d images, want %d", len(vision.requests), n)
	}
	a.bridgeImages(context.Background(), msgs, "m", nil)
	if extra := len(vision.requests) - n; extra != 0 {
		t.Fatalf("the same history re-described %d images on the next request", extra)
	}
	// Images that have left the history do not keep the cache past its
	// bound.
	a.bridgeImages(context.Background(), msgs[n-2:], "m", nil)
	a.vision.mu.Lock()
	kept := len(a.vision.seen)
	a.vision.mu.Unlock()
	if kept > maxDescribed {
		t.Fatalf("the cache kept %d descriptions for a history of 2 images", kept)
	}
}
