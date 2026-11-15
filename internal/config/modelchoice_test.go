package config

import "testing"

func TestModelChoiceKeepsProviderAndWireIDSeparate(t *testing.T) {
	cfg := &Config{Provider: "first", Model: "shared", Providers: map[string]Provider{"first": {}, "second": {}, "opencode-go": {}}}
	for _, tc := range []struct {
		arg, provider, model string
		explicit, fail       bool
	}{
		{"second shared", "second", "shared", true, false},
		{"opencode-go/kimi-k3", "opencode-go", "kimi-k3", true, false},
		{"vendor/shared", "first", "vendor/shared", false, false},
		{"unknown shared", "", "", false, true},
		{"first too many", "", "", false, true},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			p, m, explicit, err := cfg.ModelChoice(tc.arg)
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v", err)
			}
			if !tc.fail && (p != tc.provider || m != tc.model || explicit != tc.explicit) {
				t.Fatalf("choice=%s/%s explicit=%v", p, m, explicit)
			}
		})
	}
}
