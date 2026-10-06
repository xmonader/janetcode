package update

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// CheckEvery is how often Notice asks for the latest release; between
// asks it answers from the cache file.
const CheckEvery = 24 * time.Hour

type noticeCache struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest,omitempty"`
}

// Notice is the line telling the user a newer release is out, or "". A
// build that is not a release never asks, and a failed ask is remembered
// for CheckEvery like a successful one, so a machine with no network
// pays for one timeout a day, not one per start. The answer is cached in
// cacheFile.
func Notice(ctx context.Context, client *http.Client, base, current, cacheFile string, now time.Time) string {
	if _, ok := Newer(current, current); !ok {
		return ""
	}
	var c noticeCache
	if data, err := os.ReadFile(cacheFile); err == nil {
		_ = json.Unmarshal(data, &c)
	}
	if c.Checked.IsZero() || now.Sub(c.Checked) >= CheckEvery || now.Before(c.Checked) {
		if client == nil {
			client = &http.Client{Timeout: 5 * time.Second}
		}
		if tag, err := Latest(ctx, client, base); err == nil {
			c.Latest = tag
		}
		c.Checked = now
		if data, err := json.Marshal(c); err == nil {
			_ = os.MkdirAll(filepath.Dir(cacheFile), 0o755)
			_ = os.WriteFile(cacheFile, data, 0o644)
		}
	}
	if newer, ok := Newer(c.Latest, current); ok && newer {
		return "janetcode " + c.Latest + " is out (this is " + current + ") · janetcode --update installs it"
	}
	return ""
}
