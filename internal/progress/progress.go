// Package progress carries a one-line status callback through a context, so
// a long-running tool can say what it is doing without depending on the
// agent loop that shows it.
package progress

import "context"

type progressKey struct{}

// With returns a context whose Report calls reach fn. The agent
// loop sets it around every tool run so a long-running tool can say what
// it is doing — shown as the turn's status line, never as transcript.
func With(ctx context.Context, fn func(string)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// Report reports one line of progress to whoever is watching ctx. It is
// a no-op when nobody is.
func Report(ctx context.Context, line string) {
	if fn, ok := ctx.Value(progressKey{}).(func(string)); ok && fn != nil {
		fn(line)
	}
}
