# Five harnesses on laguna-s-2.1, three times each

- Model: `poolside/laguna-s-2.1` at `https://openrouter.ai/api/v1`
  ($0.09 per million input, $0.18 output at list)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Two other models'
  series ran on the same endpoint at the same time, so wall time here is
  under shared load.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **58/60** | 351,065 | **708,224** | **22,003** | **351s** | 32% |
| pi | 56/60 | **225,247** | 4,287,264 | 32,767 | 633s | 415% |
| qwencode | **58/60** | 2,216,506 | 6,793,280 | 35,846 | 701s | **4%** |
| hermes-agent | 54/60 | 314,135 | 551,808 | 27,841 | 1148s | 28% |
| openclaw | 57/60 | 2,116,739 | 6,679,392 | 31,538 | 1089s | 15% |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much. This endpoint credited cache reads to every harness on
nearly every call, and pi and hermes-agent come out at or below
janetcode's fresh figure by reading more from cache — pi six times as
much. At list price the three are within a factor of 1.7 of each other
($0.038 hermes-agent, $0.042 janetcode, $0.065 pi for 60 tasks); qwencode
and openclaw are 6x on fresh input and about $0.26 each.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 119,097 · 131,908 · 100,060 |
| pi | 22,191 · 114,195 · 88,861 |
| qwencode | 759,403 · 728,829 · 728,274 |
| hermes-agent | 90,581 · 116,022 · 107,532 |
| openclaw | 673,548 · 672,594 · 770,597 |

pi's first run spent 22k fresh and its second 114k on the same tasks — a
415% spread, and the widest of any harness on any model in these series.
Its cache reads move the other way, so the model did a similar amount of
work and the endpoint charged it differently. The two harnesses with the
largest fixed prefix, qwencode and openclaw, reproduce best, as they do
everywhere.

## What each harness got wrong

Seventeen failures in 300 runs, and eight of them are one task.

**`06-read-and-answer`** — "What does Compute(3) return? Reply with just
the number", with the function in a file beside the prompt — failed for
every harness: hermes-agent, pi and openclaw twice each, janetcode and
qwencode once. The replies on record are the model's: pi and hermes-agent
answered "I don't have enough information to determine what Compute(3)
returns" without opening the file; openclaw searched the web for it and
then replied `NO_REPLY`; pi once answered `3` and qwencode `7`, having read
the loop and miscounted. This model does not reliably look in the working
directory before answering a question about it, and the harness prompts
did not make it. 7 of 15 attempts passed.

**`17-csv-sort`** fell to hermes-agent twice (no `sorted.csv` written),
pi once and qwencode once.

**`18-find-mentions`** timed out at three minutes twice for hermes-agent.

**janetcode** also lost `16-readme` once: no `README.md` was written.
**openclaw** lost `19-write-test` once: the reverse it wrote failed the
multi-byte case of the test it wrote. **pi** lost `08-shell-script` once.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model poolside/laguna-s-2.1 --base-url https://openrouter.ai/api/v1 --out run1
```
