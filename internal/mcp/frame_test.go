package mcp

import (
	"testing"
)

// Params come from callers (a tool's arguments); one that does not encode
// is an error to return, not a panic in the client.
func TestMarshalParamsReturnsErrorInsteadOfPanicking(t *testing.T) {
	if _, err := marshalParams(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("marshalParams accepted a value json cannot encode")
	}
	raw, err := marshalParams(map[string]string{"a": "b"})
	if err != nil || string(raw) != `{"a":"b"}` {
		t.Fatalf("marshalParams = %s, %v", raw, err)
	}
}
