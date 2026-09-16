package tool

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestExtractReadableDescribesPNG(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 4))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	text, ok := extractReadable(context.Background(), "shot.png", buf.Bytes())
	if !ok || !strings.Contains(text, "8x4") || !strings.Contains(text, "png") {
		t.Fatalf("got %q ok=%v", text, ok)
	}
}

func TestExtractReadablePDFFallback(t *testing.T) {
	data := []byte("%PDF-1.4\n(hello world)\n")
	text, ok := extractReadable(context.Background(), "doc.pdf", data)
	if !ok {
		t.Fatal("expected extraction")
	}
	if !strings.Contains(text, "hello") && !strings.Contains(text, "pdf") {
		t.Fatalf("got %q", text)
	}
}
