package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoticeAsksOncePerDay(t *testing.T) {
	var asks atomic.Int32
	tag := "v0.4.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		http.Redirect(w, r, "/xmonader/janetcode/releases/tag/"+tag, http.StatusFound)
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "update.json")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	if got := Notice(ctx, nil, srv.URL, "v0.3.1", cache, now); !strings.Contains(got, "v0.4.0 is out") {
		t.Fatalf("notice = %q", got)
	}
	tag = "v0.5.0"
	if got := Notice(ctx, nil, srv.URL, "v0.3.1", cache, now.Add(time.Hour)); !strings.Contains(got, "v0.4.0") || asks.Load() != 1 {
		t.Fatalf("within a day: %q after %d asks", got, asks.Load())
	}
	if got := Notice(ctx, nil, srv.URL, "v0.3.1", cache, now.Add(25*time.Hour)); !strings.Contains(got, "v0.5.0") || asks.Load() != 2 {
		t.Fatalf("next day: %q after %d asks", got, asks.Load())
	}
	if got := Notice(ctx, nil, srv.URL, "v0.5.0", cache, now.Add(26*time.Hour)); got != "" {
		t.Fatalf("up to date: %q", got)
	}
}

// A dev build never asks; a failed ask is not retried until tomorrow.
func TestNoticeDevBuildAndOffline(t *testing.T) {
	var asks atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	cache := filepath.Join(t.TempDir(), "update.json")
	now := time.Now()
	if Notice(context.Background(), nil, srv.URL, "dev", cache, now) != "" || asks.Load() != 0 {
		t.Fatalf("dev build asked %d times", asks.Load())
	}
	Notice(context.Background(), nil, srv.URL, "v0.3.1", cache, now)
	Notice(context.Background(), nil, srv.URL, "v0.3.1", cache, now.Add(time.Minute))
	if asks.Load() != 1 {
		t.Fatalf("failed ask retried: %d asks", asks.Load())
	}
}
