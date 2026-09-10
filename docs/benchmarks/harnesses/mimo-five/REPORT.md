# Five harnesses on mimo-v2.5, three times each

- Model: `xiaomi/mimo-v2.5` at `https://openrouter.ai/api/v1`
  ($0.14 per million input, $0.003 cache read, $0.28 output at list)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Other series ran on
  the same endpoint during its first run; the model is slow regardless,
  at 25 minutes per run of 100 tasks against 4–10 for the others.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | 451,359 | **573,216** | **18,945** | **2344s** | 25% |
| pi | **60/60** | 490,355 | 3,534,336 | 20,045 | 3097s | 49% |
| qwencode | 58/60 | 2,477,630 | 5,768,768 | 21,549 | 3532s | 16% |
| hermes-agent | **60/60** | **439,712** | 645,952 | 23,743 | 2364s | 64% |
| openclaw | 59/60 | 2,533,542 | 6,558,208 | 20,611 | 3961s | **0%** |

The most accurate model of the series: 297 of 300, with janetcode, pi
and hermes-agent all perfect. janetcode, hermes-agent and pi are level on
fresh input (0.44–0.49M) and at list price cost 7.0¢, 7.0¢ and 8.5¢ for
the 60 tasks; qwencode and openclaw are 5.5x on fresh input at 37¢ and
38¢. This model's cache read is nearly free (2% of input), so the two
big-prefix harnesses pay mostly for what is not credited.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 168,182 · 134,915 · 148,262 |
| pi | 133,399 · 158,618 · 198,338 |
| qwencode | 771,352 · 897,657 · 808,621 |
| hermes-agent | 150,179 · 180,049 · 109,484 |
| openclaw | 844,949 · 843,968 · 844,625 |

openclaw's three runs are within 0.1% of each other — the tightest
reproduction of any harness on any model here. The others' prompt totals
(fresh plus cached) are just as steady — janetcode 352,310 · 339,107 ·
333,158, pi 1,356,663 · 1,305,850 · 1,362,178 — so the fresh spreads are
the endpoint's cache credit moving, not the work.

## What went wrong

Three failures in 300 runs. **qwencode** answered `7` on
`06-read-and-answer` once (the function returns 15) and failed
`18-find-mentions` once. **openclaw** wrote the nested `stack/go.mod` on
`13-multi-file-package` once, as it has on four other models.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model xiaomi/mimo-v2.5 --base-url https://openrouter.ai/api/v1 --out run1
```
