package llm

import (
	"context"
	"errors"
	"strings"
)

// ErrToolCallRejected matches a provider refusing a tool call the model
// wrote: the gateway validates the arguments against the tool's schema, or
// fails to parse them, before it relays the reply. The model produced a
// malformed call; resending the identical request tends to reproduce it,
// so the recovery is to tell the model and let it call again, not to burn
// the retry budget.
var ErrToolCallRejected = errors.New("llm: provider rejected the tool call")

// toolRejectMarks are what such a refusal says, matched case-insensitively.
var toolRejectMarks = []string{
	"tool call validation failed",
	"failed to parse tool call",
	"could not be parsed",
	"failed_generation",
	"invalid tool call",
}

// IsToolCallRejected reports whether err, by its text, is a provider
// refusing a malformed tool call.
func IsToolCallRejected(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrToolCallRejected) {
		return true
	}
	for _, text := range []string{ClassifyText(err), err.Error()} {
		l := strings.ToLower(text)
		for _, m := range toolRejectMarks {
			if strings.Contains(l, m) {
				return true
			}
		}
	}
	return false
}

type toolCallRejected struct{ err error }

func (e *toolCallRejected) Error() string        { return e.err.Error() }
func (e *toolCallRejected) Unwrap() error        { return e.err }
func (e *toolCallRejected) Is(target error) bool { return target == ErrToolCallRejected }

// AsToolCallRejected marks err as a rejected tool call when it is one, so
// errors.Is(err, ErrToolCallRejected) holds and the provider's message is
// kept. Any other error comes back unchanged.
func AsToolCallRejected(err error) error {
	if !IsToolCallRejected(err) || errors.Is(err, ErrToolCallRejected) {
		return err
	}
	return &toolCallRejected{err}
}
