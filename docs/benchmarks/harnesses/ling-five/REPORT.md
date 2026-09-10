# Five harnesses on ling-3.0-flash, three times each

- Model: `inclusionai/ling-3.0-flash` at `https://openrouter.ai/api/v1`
  ($0.021 per million input, $0.004 cache read, $0.06 output at list —
  the cheapest model in these series)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Two other models'
  series ran on the same endpoint at the same time, so wall time here is
  under shared load.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | 587,763 | **481,984** | **22,112** | **334s** | 8% |
| pi | 59/60 | 571,375 | 3,943,104 | 26,996 | 525s | 11% |
| qwencode | 57/60 | 3,624,126 | 5,624,704 | 29,291 | 871s | 8% |
| hermes-agent | 59/60 | **361,903** | 632,320 | 31,451 | 819s | 11% |
| openclaw | 56/60 | 4,631,700 | 3,173,184 | 25,578 | 1339s | **7%** |

janetcode is the only harness to pass everything, and the fastest by a
third. On fresh input hermes-agent is lowest, janetcode and pi level, and
qwencode and openclaw 6–8x. At list price the 60 tasks cost hermes-agent
1.2¢, janetcode 1.6¢, pi 2.9¢, qwencode 10¢ and openclaw 11¢ — the
whole series of 300 task runs cost under 30 cents.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 191,412 · 190,822 · 205,529 |
| pi | 184,026 · 183,885 · 203,464 |
| qwencode | 1,217,694 · 1,158,702 · 1,247,730 |
| hermes-agent | 128,886 · 115,865 · 117,152 |
| openclaw | 1,586,033 · 1,481,058 · 1,564,609 |

Every harness within 11%. This is the steadiest model of the eleven for
the benchmark's purposes: on it, a 15% change in a harness would be
visible in a single three-run series.

## What each harness got wrong

Nine failures in 300 runs.

**`17-csv-sort`** fell to qwencode all three times and to openclaw once.
qwencode passed the other 57 tasks 3/3, so this is one task the model
reliably gets wrong through that harness — the check compares the file
byte for byte, and the task directory is not kept, so which byte is not
on record.

**`06-read-and-answer`** fell to pi and openclaw once each, both
answering `10` where the function returns 15; both answered 15 in their
other two runs.

**openclaw** also lost `03-fix-failing-test` twice, with the test still
failing after its fix. **hermes-agent** ran past the three-minute budget
once on `18-find-mentions`.

**janetcode** passed 60/60.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model inclusionai/ling-3.0-flash --base-url https://openrouter.ai/api/v1 --out run1
```
