package main

import "testing"

func TestApplyPatchOfferedByModelAndSwitch(t *testing.T) {
	on := map[string]bool{}
	if !offerApplyPatch(on, "gpt-5", nil) {
		t.Error("gpt model not offered the tool")
	}
	if offerApplyPatch(on, "claude-sonnet-5-5", nil) {
		t.Error("non-patch model offered the tool")
	}
	if !offerApplyPatch(on, "local-x", []string{"local-*"}) {
		t.Error("configured model not offered the tool")
	}
	if offerApplyPatch(map[string]bool{"applypatch": true}, "gpt-5", nil) {
		t.Error("switch off still offers the tool")
	}
}
