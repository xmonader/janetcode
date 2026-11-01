package sdk_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/xmonader/janetcode/sdk"
)

func TestLoadSessionRejectsAnEmptySessionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := sdk.Start(ctx, sdk.Options{Binary: os.Args[0], Dir: t.TempDir(), Env: []string{"SDK_FAKE_ACP=1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.LoadSession(ctx, "")
	if err == nil {
		t.Fatalf("empty session id returned session %+v", s)
	}
}

func TestNewSessionRejectsAnEmptySessionID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := sdk.Start(ctx, sdk.Options{Binary: os.Args[0], Dir: t.TempDir(), Env: []string{"SDK_FAKE_ACP=1", "SDK_FAKE_ACP_EMPTY_SESSION=1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := c.NewSession(ctx)
	if err == nil {
		t.Fatalf("empty session id returned session %+v", s)
	}
}
