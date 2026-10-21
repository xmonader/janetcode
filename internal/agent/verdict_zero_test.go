package agent

import "testing"

// A runner that reports a count of zero ran nothing, whatever else its
// summary line says: cargo's "test result: ok. 0 passed", unittest's
// "Ran 0 tests" and rspec's "0 examples" are empty runs, not tests.
func TestExercisedZeroCountIsNotTested(t *testing.T) {
	for _, out := range []string{
		"running 0 tests\n\ntest result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out",
		"----------------------------------------------------------------------\nRan 0 tests in 0.000s\n\nOK",
		"Finished in 0.001 seconds\n0 examples, 0 failures",
		"OK (0 tests)",
		"Tests:       0 total",
		"0 tests, 0 assertions",
	} {
		if ran, _ := exercised(out); ran {
			t.Errorf("exercised(%q) ran=true for a run that executed no tests", out)
		}
	}
	// A cargo workspace prints a zero block for its doc tests beside the
	// real one; the real one still counts.
	if ran, _ := exercised("test result: ok. 4 passed; 0 failed\n\ntest result: ok. 0 passed; 0 failed"); !ran {
		t.Error("a cargo run with a non-empty block was not counted as tested")
	}
	for _, out := range []string{"===== 10 passed in 0.2s =====", "Ran 12 tests in 0.1s\n\nOK", "OK (10 tests)", "20 examples, 0 failures"} {
		if ran, _ := exercised(out); !ran {
			t.Errorf("exercised(%q) was not counted as tested", out)
		}
	}
}
