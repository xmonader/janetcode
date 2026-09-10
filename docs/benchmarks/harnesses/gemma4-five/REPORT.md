# Five harnesses on gemma-4-31b-it, three times each

- Model: `google/gemma-4-31b-it` at `https://openrouter.ai/api/v1`
  ($0.09 per million input, $0.05 cache read, $0.34 output at list — the
  cache discount on this model is under half, not a tenth)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Two other models'
  series ran on the same endpoint at the same time, so wall time here is
  under shared load.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | 59/60 | 868,310 | **505,457** | 17,246 | **733s** | **11%** |
| pi | 58/60 | 671,890 | 4,722,048 | 25,889 | 873s | 59% |
| qwencode | 59/60 | 3,415,968 | 5,446,132 | 19,456 | 2249s | 60% |
| hermes-agent | 54/60 | **630,691** | 663,488 | 48,383 | 1848s | 49% |
| openclaw | **60/60** | 3,877,120 | 3,248,320 | **10,485** | 2137s | 41% |

This is openclaw's best model of the series — its only 60/60 — and the
only model where janetcode's fresh input is not lowest or near it:
hermes-agent and pi are both below it. The reason is on the cache side.
janetcode's prompt total (fresh plus cached) was 1.37M for the 60 tasks
and pi's 5.39M, but this provider credited janetcode with only 37% of its
prompt as cached against pi's 88%. At list price, with the small cache
discount this model carries, janetcode and hermes-agent tie at $0.11,
pi is $0.31, openclaw $0.52 and qwencode $0.59.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 300,231 · 297,849 · 270,230 |
| pi | 288,894 · 201,613 · 181,383 |
| qwencode | 1,072,326 · 900,169 · 1,443,473 |
| hermes-agent | 217,244 · 247,648 · 165,799 |
| openclaw | 1,092,940 · 1,541,632 · 1,242,548 |

janetcode is the only harness under 40% here. pi's prompt totals per run
were 1,798,878 · 1,797,837 · 1,797,223 — identical to within 0.1% — so its
59% is entirely how much the provider chose to credit as cached in each
run, not anything pi did. The same reading does not rescue qwencode and
openclaw, whose totals moved with their fresh figures.

## What each harness got wrong

Ten failures in 300 runs.

**hermes-agent** lost six: `11-go-return-error` twice and
`13-multi-file-package` once by running past the three-minute budget,
`02-go-add-function` with a test for a function it never wrote,
`03-fix-failing-test` with the test still failing, and `17-csv-sort` once.

**pi** lost `08-shell-script`, reading a file that was not there, and
`17-csv-sort`, once each.

**janetcode** and **qwencode** each lost `06-read-and-answer` once — the
one task judged from the final message — and passed everything else.

**openclaw** passed everything.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model google/gemma-4-31b-it --base-url https://openrouter.ai/api/v1 --out run1
```
