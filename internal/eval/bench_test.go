package eval

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBatchesOfFiveFromEvalDir(t *testing.T) {
	dir := filepath.Join("..", "..", "evals")
	tasks, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := Filter01to20(tasks)
	if len(got) != 20 {
		t.Fatalf("01-20 count = %d, want 20", len(got))
	}
	if TaskIndex(got[0].Name) != 1 || got[0].Name != "01-create-file" {
		t.Fatalf("first = %q", got[0].Name)
	}
	if got[19].Name != "20-env-default" {
		t.Fatalf("last = %q", got[19].Name)
	}
	for _, tsk := range got {
		if TaskIndex(tsk.Name) > 20 {
			t.Fatalf("hard set leaked: %s", tsk.Name)
		}
	}
	batches := BatchesOfFive(got)
	if len(batches) != 4 {
		t.Fatalf("batches = %d, want 4", len(batches))
	}
	want := [][]string{
		{"01-create-file", "02-go-add-function", "03-fix-failing-test", "04-python-fizzbuzz", "05-rename-symbol"},
		{"06-read-and-answer", "07-json-config-edit", "08-shell-script", "09-makefile-target", "10-append-preserve"},
		{"11-go-return-error", "12-python-bugfix", "13-multi-file-package", "14-gitignore", "15-yes-no-question"},
		{"16-readme", "17-csv-sort", "18-find-mentions", "19-write-test", "20-env-default"},
	}
	for i, batch := range batches {
		if len(batch) != 5 {
			t.Fatalf("batch %d len = %d, want 5", i+1, len(batch))
		}
		for j, name := range want[i] {
			if batch[j].Name != name {
				t.Fatalf("batch %d task %d = %q, want %q", i+1, j, batch[j].Name, name)
			}
		}
	}
}

func TestAggregateReportDoesNotHardcodeTotals(t *testing.T) {
	dir := filepath.Join("..", "..", "evals")
	tasks, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks = Filter01to20(tasks)
	var results []BenchTaskResult
	sumIn, sumCache, sumOut := 0, 0, 0
	var sumWall time.Duration
	for i, tsk := range tasks {
		in, cache, out := 11*(i+1), 3*(i+1), 7*(i+1)
		wall := time.Duration(i+1) * 250 * time.Millisecond
		sumIn += in
		sumCache += cache
		sumOut += out
		sumWall += wall
		r := BenchTaskResult{
			Harness:          "pi",
			Task:             tsk.Name,
			Batch:            i/5 + 1,
			Passed:           i%2 == 0,
			InputTokens:      Int(in),
			CacheReadTokens:  Int(cache),
			CacheWriteTokens: Int(0),
			OutputTokens:     Int(out),
			ToolCalls:        Int(i + 1),
			Turns:            Int(2),
		}
		r.SetWall(wall)
		results = append(results, r)
	}
	tot := AggregateHarness(results)
	if tot.Input.Value != sumIn || !tot.Input.Set {
		t.Fatalf("input total = %+v, want %d", tot.Input, sumIn)
	}
	if tot.CacheRead.Value != sumCache || !tot.CacheRead.Set {
		t.Fatalf("cache total = %+v, want %d", tot.CacheRead, sumCache)
	}
	if tot.Output.Value != sumOut || !tot.Output.Set {
		t.Fatalf("output total = %+v, want %d", tot.Output, sumOut)
	}
	if tot.WallTime != sumWall {
		t.Fatalf("wall = %s, want %s", tot.WallTime, sumWall)
	}
	if tot.Tasks != 20 || tot.Passed != 10 {
		t.Fatalf("pass/tasks = %d/%d", tot.Passed, tot.Tasks)
	}

	missing := BenchTaskResult{Harness: "openclaw", Task: "01-create-file", Batch: 1, Unavailable: true, UnavailableReason: "node version"}
	na := AggregateHarness([]BenchTaskResult{missing})
	if na.Input.Set || na.Output.Set || na.CacheRead.Set {
		t.Fatalf("unavailable harness invented tokens: %+v", na)
	}

	report := RenderComparativeReport(map[string][]BenchTaskResult{
		"pi":       results,
		"openclaw": UnavailableResults("openclaw", "node >=22.22.3 required", "openclaw --help", "current: v22.22.2", tasks),
	}, ReportMeta{Model: "qwen3.7-plus", Endpoint: "https://coding-intl.dashscope.aliyuncs.com/v1", When: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)})
	for _, needle := range []string{"input tokens", "cache read tokens", "output tokens", "wall time", "qwen3.7-plus"} {
		if !strings.Contains(strings.ToLower(report), needle) && !strings.Contains(report, needle) {
			t.Fatalf("report missing %q\n%s", needle, report)
		}
	}
	if !strings.Contains(report, "input tokens") || !strings.Contains(report, "cache read tokens") || !strings.Contains(report, "output tokens") || !strings.Contains(report, "wall time") {
		t.Fatalf("report must name input / cache / output tokens and wall time:\n%s", report)
	}
	if !strings.Contains(report, itoa(sumIn)) {
		t.Fatalf("report missing computed input total %d", sumIn)
	}
	if !strings.Contains(report, itoa(sumCache)) {
		t.Fatalf("report missing computed cache total %d", sumCache)
	}
	if !strings.Contains(report, itoa(sumOut)) {
		t.Fatalf("report missing computed output total %d", sumOut)
	}
	if strings.Contains(report, "sk-sp-") {
		t.Fatal("report leaked a key prefix")
	}

	dirOut := t.TempDir()
	path := filepath.Join(dirOut, "pi.json")
	if err := WriteBenchJSON(path, results); err != nil {
		t.Fatal(err)
	}
	back, err := ReadBenchJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 20 || back[0].InputTokens.Value != results[0].InputTokens.Value {
		t.Fatalf("roundtrip = %+v", back[0])
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestParseUsageJSONAndText(t *testing.T) {
	pi := []byte(`{"type":"assistant","message":{"role":"assistant","usage":{"input":10,"output":4,"cacheRead":2,"cacheWrite":1,"cost":{"total":0.01}}}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}],"usage":{"input":5,"output":3,"cacheRead":0,"cacheWrite":0}}}`)
	u := ParseUsageJSON(pi)
	if !u.Input.Set || u.Input.Value != 15 || u.Output.Value != 7 || u.CacheRead.Value != 2 || u.CacheWrite.Value != 1 {
		t.Fatalf("pi usage = %+v", u)
	}
	if got := ExtractAssistantText(pi); !strings.Contains(got, "hello world") {
		t.Fatalf("pi text = %q", got)
	}

	grok := []byte(`{"text":"pong","stopReason":"end_turn","num_turns":2,"usage":{"input_tokens":8,"cache_read_input_tokens":40,"cache_creation_input_tokens":1,"output_tokens":3},"total_cost_usd":0.002}`)
	u = ParseUsageJSON(grok)
	if u.Input.Value != 8 || u.CacheRead.Value != 40 || u.CacheWrite.Value != 1 || u.Output.Value != 3 || !u.Turns.Set || u.Turns.Value != 2 {
		t.Fatalf("grok usage = %+v", u)
	}
	if got := ExtractAssistantText(grok); got != "pong" {
		t.Fatalf("grok text = %q", got)
	}

	qwen := []byte(`[{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"x"}]}},{"type":"result","subtype":"success","result":"15","usage":{"input_tokens":100,"output_tokens":9},"stats":{"tools":{"totalCalls":3}}}]`)
	u = ParseUsageJSON(qwen)
	if u.Input.Value != 100 || u.Output.Value != 9 || u.ToolCalls.Value != 3 {
		t.Fatalf("qwen usage = %+v", u)
	}
	if got := ExtractAssistantText(qwen); got != "15" {
		t.Fatalf("qwen text = %q", got)
	}

	text := "  Input tokens:              1,234\n  Output tokens:             56\n  API Calls: 4\n"
	u = ParseUsageText(text)
	if u.Input.Value != 1234 || u.Output.Value != 56 || u.Turns.Value != 4 {
		t.Fatalf("text usage = %+v", u)
	}

	hermes := `
22:53:31 - agent.conversation_loop - INFO - API call #3: model=qwen3.7-plus provider=unknown in=3500 out=80 total=3580 latency=2.0s cache=1024/3500 (29%)
22:53:31 - agent.conversation_loop - INFO [20260908_225311_ad8b1e] - API call #4: model=qwen3.7-plus provider=unknown in=3741 out=64 total=3805 latency=3.1s cache=2048/3741 (55%)
22:53:31 - root - DEBUG [20260908_225311_ad8b1e] - Token usage: prompt=3,741, completion=64, total=3,805
`
	u = ParseUsageText(hermes)
	// hermes counts the cached part inside in=; input is the uncached rest.
	if !u.Input.Set || u.Input.Value != (3500-1024)+(3741-2048) || u.Output.Value != 80+64 || u.CacheRead.Value != 1024+2048 || u.Turns.Value != 2 {
		t.Fatalf("hermes usage = %+v", u)
	}
	// qwen-code stats: "prompt" includes "cached".
	qstats := ParseUsageJSON([]byte(`{"type":"result","stats":{"models":{"m":{"tokens":{"prompt":1000,"cached":600,"candidates":50}}}}}`))
	if !qstats.Input.Set || qstats.Input.Value != 400 || qstats.CacheRead.Value != 600 || qstats.Output.Value != 50 {
		t.Fatalf("qwen stats usage = %+v", qstats)
	}

	saved := filepath.Join("..", "..", "docs", "benchmarks", "harnesses", "hermes-agent.json")
	if rs, err := ReadBenchJSON(saved); err == nil && len(rs) > 0 && strings.Contains(rs[0].StderrExcerpt, "in=3741") {
		got := ParseUsageText(rs[0].StderrExcerpt)
		if !got.Input.Set || got.Input.Value != 3741-2048 || got.Output.Value != 64 || got.CacheRead.Value != 2048 {
			t.Fatalf("saved hermes 01 excerpt = %+v", got)
		}
	}

	empty := ParseUsageJSON([]byte(`{"ok":true}`))
	if empty.HasTokens() {
		t.Fatalf("empty invented tokens: %+v", empty)
	}
}

func TestUnavailableCoversAllTwenty(t *testing.T) {
	dir := filepath.Join("..", "..", "evals")
	tasks, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	tasks = Filter01to20(tasks)
	rs := UnavailableResults("grok", "cannot target Dashscope", "grok -p ...", "default model: grok-4.6", tasks)
	if len(rs) != 20 {
		t.Fatalf("len=%d", len(rs))
	}
	if rs[0].Batch != 1 || rs[5].Batch != 2 || rs[19].Batch != 4 || !rs[0].Unavailable {
		t.Fatalf("%+v / %+v", rs[0], rs[19])
	}
}

func TestLargeSetDistinctFromSmallEvals(t *testing.T) {
	small, err := Load(filepath.Join("..", "..", "evals"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tsk := range small {
		if TaskIndex(tsk.Name) >= LargeTaskMin {
			t.Fatalf("large task in evals/: %s", tsk.Name)
		}
	}
	tasks, err := Load(filepath.Join("..", "..", "evals", "large"))
	if err != nil {
		t.Fatal(err)
	}
	got := FilterLarge(tasks)
	if len(got) != 5 {
		t.Fatalf("large count = %d, want 5", len(got))
	}
	for i, name := range LargeTaskNames {
		if got[i].Name != name {
			t.Fatalf("task %d = %q, want %q", i, got[i].Name, name)
		}
		if TaskIndex(name) <= 20 || TaskIndex(name) == 21 {
			t.Fatalf("collides with small/hard set: %s", name)
		}
		if len(got[i].Files) < largeFiles/2 {
			t.Fatalf("%s corpus too small: %d files", name, len(got[i].Files))
		}
	}
	for _, h := range LargeHarnesses {
		if h == "openclaw" {
			t.Fatal("openclaw must not be in the large set")
		}
	}
	if len(BatchesOfFive(got)) != 1 || len(BatchesOfFive(got)[0]) != 5 {
		t.Fatalf("large batches = %+v", BatchesOfFive(got))
	}
}

func TestLargeAggregateNearMillionTokens(t *testing.T) {
	var results []BenchTaskResult
	sumIn, sumCache, sumOut := 0, 0, 0
	var sumWall time.Duration
	for i, name := range LargeTaskNames {
		in, cache, out := 900_000+i*111, 120_000+i*7, 4_000+i
		wall := time.Duration(i+1) * time.Minute
		sumIn += in
		sumCache += cache
		sumOut += out
		sumWall += wall
		r := BenchTaskResult{
			Harness:         "janetcode",
			Task:            name,
			Batch:           1,
			Passed:          true,
			InputTokens:     Int(in),
			CacheReadTokens: Int(cache),
			OutputTokens:    Int(out),
		}
		r.SetWall(wall)
		results = append(results, r)
	}
	tot := AggregateHarness(results)
	if tot.Input.Value != sumIn || tot.CacheRead.Value != sumCache || tot.Output.Value != sumOut || tot.WallTime != sumWall {
		t.Fatalf("totals = in %d cache %d out %d wall %s", tot.Input.Value, tot.CacheRead.Value, tot.Output.Value, tot.WallTime)
	}
	if tot.Input.Value < 1_000_000 {
		t.Fatalf("synthesized session usage should be near 1e6, got %d", tot.Input.Value)
	}
	report := RenderComparativeReport(map[string][]BenchTaskResult{"janetcode": results, "pi": results}, ReportMeta{
		Model: "qwen3.7-plus", Endpoint: "https://coding-intl.dashscope.aliyuncs.com/v1",
		Tasks: "31–35 (five large evals, one batch of five); openclaw excluded",
	})
	for _, n := range []string{"input tokens", "cache read tokens", "output tokens", "wall time"} {
		if !strings.Contains(report, n) {
			t.Fatalf("report missing %q", n)
		}
	}
	if !strings.Contains(report, itoa(sumIn)) || !strings.Contains(report, itoa(sumCache)) {
		t.Fatalf("report missing computed totals")
	}
	if !strings.Contains(report, "31–35") {
		t.Fatal("large report header must name 31–35")
	}
	if strings.Contains(report, "01–20") {
		t.Fatal("large report must not claim the 01–20 suite")
	}
	if strings.Contains(report, "| openclaw |") {
		t.Fatal("large report must not include an openclaw row")
	}
}

func TestMergeBenchResultsKeepsOtherTasks(t *testing.T) {
	old := []BenchTaskResult{
		{Harness: "janetcode", Task: "31-large-inventory", Passed: true, InputTokens: Int(10)},
		{Harness: "janetcode", Task: "32-large-rename", Passed: true, InputTokens: Int(11)},
		{Harness: "janetcode", Task: "33-large-sum", Passed: true, InputTokens: Int(12)},
	}
	neu := []BenchTaskResult{
		{Harness: "janetcode", Task: "32-large-rename", Passed: false, InputTokens: Int(99)},
		{Harness: "janetcode", Task: "34-large-headers", Passed: true, InputTokens: Int(13)},
	}
	got := MergeBenchResults(old, neu)
	if len(got) != 4 {
		t.Fatalf("len=%d want 4: %+v", len(got), got)
	}
	if got[0].Task != "31-large-inventory" || got[0].InputTokens.Value != 10 {
		t.Fatalf("kept 31: %+v", got[0])
	}
	if got[1].Task != "32-large-rename" || got[1].Passed || got[1].InputTokens.Value != 99 {
		t.Fatalf("replaced 32: %+v", got[1])
	}
	if got[2].Task != "33-large-sum" || got[3].Task != "34-large-headers" {
		t.Fatalf("order: %s %s", got[2].Task, got[3].Task)
	}
}
