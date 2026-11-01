package llm

import "testing"

func TestRequestValidateRejectsUnknownRoles(t *testing.T) {
	req := Request{Model: "m", Messages: []Message{
		TextMessage(RoleUser, "x"),
		TextMessage(Role("system"), "not a conversation role"),
	}}
	if err := req.Validate(); err == nil {
		t.Fatal("unknown message role accepted")
	}
}
