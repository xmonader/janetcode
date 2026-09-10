# Five harnesses on deepseek-v4-pro-0813, three times each

- Model: `deepseek/deepseek-v4-pro-0813` at `https://openrouter.ai/api/v1`
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md)

Why this build: the newest DeepSeek on OpenRouter that day,
`deepseek-v4.1-flash`, has a single endpoint that trains on prompts, and
the account's privacy setting excludes such endpoints, so every request
returns 404. DeepSeek's own API answered "insufficient balance". This is
the newest DeepSeek the account could reach.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | 311,325 | **674,048** | **17,460** | **698s** | 3% |
| pi | **60/60** | **146,516** | 4,164,096 | 20,703 | 912s | 26% |
| qwencode | **60/60** | 2,309,218 | 6,123,264 | 27,352 | 1405s | **1%** |
| hermes-agent | **60/60** | 321,281 | 681,984 | 20,958 | 970s | 3% |
| openclaw | 59/60 | 2,292,517 | 5,397,504 | 22,712 | 1759s | 21% |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much. On this endpoint every harness was credited with cache
reads on nearly every call.

This is the one model in the series where janetcode does not have the
lowest fresh-input figure: pi does, at half of janetcode's, and it gets
there by reading 4.2M from cache to janetcode's 674k — six times as much.
At OpenRouter's listed prices for this model ($1.05 per million fresh,
cache reads at a fraction of that) the two come out close, with the
ordering depending on the cache discount; janetcode is the cheaper on
output and the faster in wall time, pi on fresh input. hermes-agent and
janetcode are near-identical on every column. qwencode and openclaw are
7.4x on fresh input, as their per-call prefix predicts.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 103,605 · 102,229 · 105,491 |
| pi | 47,039 · 43,921 · 55,556 |
| qwencode | 771,994 · 767,968 · 769,256 |
| hermes-agent | 109,093 · 105,416 · 106,772 |
| openclaw | 713,799 · 714,490 · 864,228 |

Three harnesses reproduce to 3% or better. openclaw's third run spent
103k on `03-fix-failing-test` where its first two spent 37k on their
largest task, which is the whole of its 21%.

## What went wrong

One failure in 300: openclaw on `06-read-and-answer` in the second run.
Its reply was "Compute(3) = 3³ - 1 = 26": the model guessed a formula
instead of reading the loop, which returns 15. The reply was read from its
result object and scored as written. Every other cell passed three out of
three.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model deepseek/deepseek-v4-pro-0813 --base-url https://openrouter.ai/api/v1 --out run1
```
