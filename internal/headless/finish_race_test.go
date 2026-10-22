package headless

import (
	"bytes"
	"sync"
	"testing"
)

// The final result and a stdin reader's input_error line write the same
// stdout; under -race the result's unlocked write was reported.
func TestFinishWritesUnderTheRecorderLock(t *testing.T) {
	for i := 0; i < 20; i++ {
		var out bytes.Buffer
		r := newRecorder(Options{Stdout: &out, Stderr: &bytes.Buffer{}}, FormatStreamJSON)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			r.finish(nil, nil)
		}()
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 20; j++ {
				r.emitLocked(streamEvent{Type: "input_error", Text: "bad line"})
			}
		}()
		close(start)
		wg.Wait()
	}
}
