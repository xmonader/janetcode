package tool

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestComputerScreenshotWritesPNG(t *testing.T) {
	root := t.TempDir()
	png := fakePNG(32, 16)
	c := Computer{Root: root, Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "grim" || name == "import" || name == "screencapture" || name == "scrot" {
			if name == "screencapture" || name == "scrot" {
				os.WriteFile(args[len(args)-1], png, 0o644)
			}
			return png, nil
		}
		return nil, fmt.Errorf("unexpected %s", name)
	}}
	out, err := c.Run(context.Background(), raw(`{"action":"screenshot"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "32x16") || !strings.Contains(out, ".janetcode/screenshots/") {
		t.Fatalf("out = %q", out)
	}
}

func TestComputerClickAndType(t *testing.T) {
	var got []string
	c := Computer{Exec: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append(got, name+" "+strings.Join(args, " "))
		return nil, nil
	}}
	if _, err := c.Run(context.Background(), raw(`{"action":"click","x":10,"y":20}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), raw(`{"action":"type","text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(context.Background(), raw(`{"action":"key","key":"Return"}`)); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no commands ran")
	}
}

func TestComputerGuardAsksForInput(t *testing.T) {
	c := Computer{}
	if ask, _ := c.Guard(raw(`{"action":"screenshot"}`)); ask {
		t.Fatal("screenshot should not force a prompt under yolo")
	}
	if ask, reason := c.Guard(raw(`{"action":"click","x":1,"y":1}`)); !ask || reason == "" {
		t.Fatalf("click should always ask: %v %q", ask, reason)
	}
}

func TestComputerUnknownAction(t *testing.T) {
	c := Computer{}
	if _, err := c.Run(context.Background(), raw(`{"action":"drag"}`)); err == nil {
		t.Fatal("unknown action should fail")
	}
}

// grim and import write the PNG to stdout; a warning they print on stderr
// must not end up inside the image.
func TestComputerRunKeepsStderrOutOfTheOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	out, err := Computer{}.run(context.Background(), "/bin/sh", "-c", "printf image; printf warning >&2")
	if err != nil || string(out) != "image" {
		t.Fatalf("out = %q, %v", out, err)
	}
	_, err = Computer{}.run(context.Background(), "/bin/sh", "-c", "printf why >&2; exit 3")
	if err == nil || !strings.Contains(err.Error(), "why") {
		t.Fatalf("a failure lost its stderr: %v", err)
	}
}

func fakePNG(w, h int) []byte {
	buf := bytes.Repeat([]byte{0}, 24)
	buf[0] = 0x89
	copy(buf[1:4], "PNG")
	binary.BigEndian.PutUint32(buf[16:20], uint32(w))
	binary.BigEndian.PutUint32(buf[20:24], uint32(h))
	return buf
}

func TestPNGSize(t *testing.T) {
	w, h, ok := pngSize(fakePNG(640, 480))
	if !ok || w != 640 || h != 480 {
		t.Fatalf("%d %d %v", w, h, ok)
	}
}
