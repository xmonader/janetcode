package numclaim

import (
	"strings"
	"testing"
)

func texts(cs []Claim) []string {
	var out []string
	for _, c := range cs {
		out = append(out, string(c.Kind)+":"+c.Text)
	}
	return out
}

func TestExtractFindsMeasuredNumbers(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Parsing is now 40% faster.", "speed:40%"},
		{"The new loop is 2.5x faster than before", "speed:2.5x"},
		{"Allocations halved in the hot path.", "memory:halved"},
		{"Coverage is 85% for the package.", "coverage:85%"},
		{"BenchmarkParse now runs at 120 ns/op", "speed:120 ns/op"},
		{"It went from 3 allocs/op to 1 allocs/op.", "memory:3 allocs/op,memory:1 allocs/op"},
		{"The binary size dropped to 4.2 MB", "size:4.2 MB"},
		{"| Parse | 340 ns | 210 ns | -38% faster |", "speed:340 ns,speed:210 ns,speed:-38%"},
		{"Memory use is about 1,200 KB now.", "memory:1,200 KB"},
		{"Twice as fast on large inputs.", "speed:Twice as fast"},
	}
	for _, c := range cases {
		got := strings.Join(texts(Extract(c.text)), ",")
		if got != c.want {
			t.Errorf("Extract(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}

func TestExtractLeavesOtherNumbersAlone(t *testing.T) {
	for _, text := range []string{
		"Upgraded to Go 1.22 and fixed line 40.",
		"All 42 tests pass.",
		"This could be 2x faster with a cache.",
		"The task asks for at least 80% coverage.",
		"Reduced the function by 40%.", // no word says what was measured
		"Uses 0x1F as the mask, 1080x720 frames.",
		"```\nBenchmarkParse-8 1000 1234 ns/op\n```",
		"> the old code was 3x slower",
		"Version v1.5s of the tool.",
		"It should take under 5 ms.",
		// From recorded sessions: durations that are not speed claims.
		"All 125 tests pass (0.21s).",
		"asc and desc requests shared a cache entry for 30s.",
		"The search is bounded by 8×366 days.",
	} {
		if got := Extract(text); len(got) != 0 {
			t.Errorf("Extract(%q) = %v, want none", text, texts(got))
		}
	}
}

func TestExtractDecimalIsNotASentenceEnd(t *testing.T) {
	got := Extract("Now 1.5x faster. Coverage 91.2% overall.")
	if strings.Join(texts(got), ",") != "speed:1.5x,coverage:91.2%" {
		t.Fatalf("got %v", texts(got))
	}
}

const (
	benchOld = "goos: linux\nBenchmarkParse-8   	 1000000	      1000 ns/op	     480 B/op	       6 allocs/op\nPASS\n"
	benchNew = "goos: linux\nBenchmarkParse-8   	 2000000	       600 ns/op	     240 B/op	       3 allocs/op\nPASS\n"
)

func bench(out string) Step {
	return Step{Cmd: "go test -bench=Parse -benchmem ./parse", Output: out}
}

var edit = Step{Edit: true}

func status(t *testing.T, text string, steps ...Step) Status {
	t.Helper()
	cs := Extract(text)
	if len(cs) != 1 {
		t.Fatalf("Extract(%q) = %v, want one claim", text, texts(cs))
	}
	return Check(cs, steps)[0].Status
}

func TestCheckSpeedNeedsBeforeAndAfter(t *testing.T) {
	if s := status(t, "Parse is 40% faster.", bench(benchOld), edit, bench(benchNew)); s != Backed {
		t.Errorf("before+after giving 40%%: %s", s)
	}
	// (1000-600)/600 = 66.7%: the other way people write it.
	if s := status(t, "Parse is 67% faster.", bench(benchOld), edit, bench(benchNew)); s != Backed {
		t.Errorf("67%% from the after side: %s", s)
	}
	if s := status(t, "Parse is 1.67x faster.", bench(benchOld), edit, bench(benchNew)); s != Backed {
		t.Errorf("1.67x: %s", s)
	}
	if s := status(t, "Parse is 40% faster.", edit, bench(benchNew)); s != NoBaseline {
		t.Errorf("after only: %s, want no baseline", s)
	}
	if s := status(t, "Parse is 40% faster.", bench(benchOld), edit); s != Stale {
		t.Errorf("before only: %s, want stale", s)
	}
	if s := status(t, "Parse is 40% faster.", edit, Step{Cmd: "go test ./..."}); s != Unmeasured {
		t.Errorf("tests only: %s, want unmeasured", s)
	}
	if s := status(t, "Parse is 90% faster.", bench(benchOld), edit, bench(benchNew)); s != NotFound {
		t.Errorf("an invented number: %s, want not found", s)
	}
}

func TestCheckMemoryClaims(t *testing.T) {
	if s := status(t, "Allocations halved.", bench(benchOld), edit, bench(benchNew)); s != Backed {
		t.Errorf("halved allocs: %s", s)
	}
	if s := status(t, "Parse now does 3 allocs/op.", edit, bench(benchNew)); s != Backed {
		t.Errorf("a level after the change: %s", s)
	}
	if s := status(t, "Parse now does 2 allocs/op.", edit, bench(benchNew)); s != NotFound {
		t.Errorf("a level nothing printed: %s", s)
	}
}

func TestCheckCoverage(t *testing.T) {
	cov := Step{Cmd: "go test -cover ./parse", Output: "ok  	example/parse	0.01s	coverage: 84.7% of statements\n"}
	if s := status(t, "Coverage is 85% now.", edit, cov); s != Backed {
		t.Errorf("rounded coverage: %s", s)
	}
	if s := status(t, "Coverage is 95% now.", edit, cov); s != NotFound {
		t.Errorf("wrong coverage: %s", s)
	}
	if s := status(t, "Coverage is 85% now.", cov, edit); s != Stale {
		t.Errorf("coverage before the edit: %s", s)
	}
}

func TestCheckComparisonRuns(t *testing.T) {
	stat := Step{Cmd: "benchstat old.txt new.txt", Output: "        │ old.txt │ new.txt │\n        │ sec/op  │ sec/op  vs base │\nParse-8   1.000µ ± 1%   0.600µ ± 2%  -40.00% (p=0.000 n=10)\n"}
	if s := status(t, "Parse is 40% faster.", edit, stat); s != Backed {
		t.Errorf("benchstat states it: %s", s)
	}
	noise := Step{Cmd: "benchstat old.txt new.txt", Output: "Parse-8   1.000µ ± 9%   0.950µ ± 8%  ~ (p=0.393 n=10)\n"}
	if s := status(t, "Parse is 5% faster.", edit, noise); s != NotSignificant {
		t.Errorf("benchstat says ~: %s, want not significant", s)
	}
	hf := Step{Cmd: `hyperfine 'old/app input' 'new/app input'`, Output: "Summary\n  'new/app input' ran\n    1.95 ± 0.05 times faster than 'old/app input'\n"}
	if s := status(t, "The new build is 2x faster.", edit, hf); s != Backed {
		t.Errorf("hyperfine states it: %s", s)
	}
}

func TestCheckPairsReadingsByBenchmark(t *testing.T) {
	old := "BenchmarkA-8 100 1000 ns/op\nBenchmarkB-8 100 500 ns/op\n"
	cur := "BenchmarkA-8 100 900 ns/op\nBenchmarkB-8 100 450 ns/op\n"
	// 1000 → 500 would be 50%, but those are two benchmarks.
	if s := status(t, "Everything is 50% faster.", bench(old), edit, bench(cur)); s != NotFound {
		t.Errorf("cross-benchmark pair: %s, want not found", s)
	}
	if s := status(t, "Both are 10% faster.", bench(old), edit, bench(cur)); s != Backed {
		t.Errorf("10%%: %s", s)
	}
}

func TestCheckShellTime(t *testing.T) {
	before := Step{Cmd: "time ./build.sh", Output: "real\t0m2.000s\nuser\t0m1.0s\n"}
	after := Step{Cmd: "time ./build.sh", Output: "real\t0m1.000s\nuser\t0m0.5s\n"}
	if s := status(t, "The build is 2x faster.", before, edit, after); s != Backed {
		t.Errorf("time: %s", s)
	}
}

func TestFailedRunMeasuresNothing(t *testing.T) {
	broken := Step{Cmd: "go test -bench=. ./parse", Output: "build failed", Failed: true}
	if s := status(t, "Parse is 40% faster.", broken, edit, broken); s != Unmeasured {
		t.Errorf("failed runs: %s, want unmeasured", s)
	}
}

func TestSummary(t *testing.T) {
	fs := Check(Extract("Parse is 40% faster. Coverage is 85%."), []Step{edit, {Cmd: "go test ./..."}})
	if Unbacked(fs) != 2 {
		t.Fatalf("unbacked = %d", Unbacked(fs))
	}
	s := Summary(fs)
	if !strings.Contains(s, `"40%": no benchmark or timing run`) || !strings.Contains(s, `"85%": no coverage run`) {
		t.Errorf("summary: %s", s)
	}
}
