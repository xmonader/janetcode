package sdk

import (
	"encoding/base64"
	"time"
)

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// waitTimeout is closed if done has not closed within a few seconds: the
// grace a process gets to finish writing its session after stdin closes.
func waitTimeout(done <-chan struct{}) <-chan struct{} {
	out := make(chan struct{})
	go func() {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			close(out)
		}
	}()
	return out
}
