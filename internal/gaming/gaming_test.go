package gaming

import "testing"

func TestScanAlwaysTrueEquality(t *testing.T) {
	got := Scan([]Delta{{
		Path:  "mod.py",
		Added: []string{"def __eq__(self, other):", "    return True"},
	}})
	if len(got) != 1 || got[0].Kind != "always-true-equality" {
		t.Fatalf("%+v", got)
	}
}

func TestScanCallCount(t *testing.T) {
	got := Scan([]Delta{{
		Path: "mod.py",
		Added: []string{
			"self._n += 1",
			"if self._n == 1:",
			"    return 'even'",
			"return 'odd'",
		},
	}})
	if len(got) != 1 || got[0].Kind != "call-count-dependent-answer" {
		t.Fatalf("%+v", got)
	}
}

func TestScanRewrittenExpectation(t *testing.T) {
	got := Scan([]Delta{{
		Path:    "tests/test_foo.py",
		Removed: []string{"assert result == 4"},
		Added:   []string{"assert result == 5"},
	}})
	if len(got) != 1 || got[0].Kind != "rewritten-test-expectation" {
		t.Fatalf("%+v", got)
	}
}

func TestScanIgnoresQuotedSkip(t *testing.T) {
	got := Scan([]Delta{{
		Path:  "tests/test_foo.py",
		Added: []string{`raise ValueError("use @pytest.mark.skip")`},
	}})
	if len(got) != 0 {
		t.Fatalf("quoted skip: %+v", got)
	}
}
