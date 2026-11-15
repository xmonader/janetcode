package llm

import "context"

type sessionIDKey struct{}

// WithSessionID gives model gateways a stable conversation identifier.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, id)
}

func SessionID(ctx context.Context) string {
	id, _ := ctx.Value(sessionIDKey{}).(string)
	return id
}
