package weburl

import "testing"

func TestSearchConfigValidate(t *testing.T) {
	if err := (SearchConfig{Provider: "brave"}).Validate(); err == nil {
		t.Fatal("brave without key validated")
	}
	if err := (SearchConfig{Provider: "bing"}).Validate(); err == nil {
		t.Fatal("unknown provider validated")
	}
	if err := (SearchConfig{}).Validate(); err != nil {
		t.Fatal(err)
	}
}
