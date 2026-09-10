# Five harnesses on qwen3.8-flash, three times each

- Model: `qwen/qwen3.8-flash` at `https://openrouter.ai/api/v1`
  ($0.15 per million input, $0.016 cache read, $0.47 output at list)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). This series ran on
  its own after the others: the model was rate-limited upstream when
  seven smoke tests hit it at once, and openclaw refuses to start on a
  429. Alone, no run hit the limit.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | 59/60 | 379,891 | 918,436 | 36,929 | 1246s | **12%** |
| pi | 56/60 | 233,878 | 5,661,824 | 50,075 | 1412s | 25% |
| qwencode | 57/60 | 2,036,414 | 9,897,472 | 62,084 | 2146s | 42% |
| hermes-agent | **60/60** | **204,660** | **862,976** | **25,820** | **1158s** | 19% |
| openclaw | 56/60 | 851,308 | 6,592,192 | 29,833 | 1700s | 34% |

Alibaba's newest flash model, two weeks old at the time. It is a
reasoning model and every harness's output column is up — qwencode
writes 62k tokens against 25–30k on its predecessor — and its cache credit
is generous: openclaw, credited 89% of its prompt, is only 2.2x janetcode
on fresh input here, against 4–9x elsewhere.

hermes-agent has its best model of the series: 60/60, lowest on every
column, 5.9¢ at list price for the 60 tasks against janetcode's 8.9¢, pi's
15¢, openclaw's 25¢ and qwencode's 49¢.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 133,864 · 119,304 · 126,723 |
| pi | 87,906 · 70,061 · 75,911 |
| qwencode | 579,880 · 824,499 · 632,035 |
| hermes-agent | 62,210 · 68,543 · 73,907 |
| openclaw | 259,404 · 253,087 · 338,817 |

## What each harness got wrong

Thirteen failures in 300 runs. **`17-csv-sort`** took five of them:
qwencode twice, janetcode, pi and openclaw once each. `03-fix-failing-test`
fell to pi and openclaw once each with the test still failing.

**pi** also lost `08-shell-script` and `06-read-and-answer`, where its
final message was a directory listing rather than the number.
**openclaw** lost `05-rename-symbol` once and ran past three minutes on
`11-go-return-error`. **qwencode** lost `18-find-mentions` once.
**hermes-agent** passed everything.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen/qwen3.8-flash --base-url https://openrouter.ai/api/v1 --out run1
```
