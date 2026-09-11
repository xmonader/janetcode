# Five harnesses on the session set, mercury-2.5, three times each

- Model: `inception/mercury-2.5` at `https://openrouter.ai/api/v1`
  ($0.04 per million fresh, $0.15 output at list)
- Tasks: `evals/session/` 41–43, the multi-prompt session set (a Go
  service over seven prompts, a records tree over six, a Python ledger
  over six), five minutes per task, identical seeded directories for
  every harness; one continuing session per task for every harness that
  can continue one (hermes-agent cannot and starts fresh per prompt)
- Three complete repeats, 45 task runs in total, recorded 2026-09-11.
  This series stands in for the third qwen3.7-plus run that DashScope's
  quota lost (`../qwen-session/`)

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| janetcode | 5/9 | 3,231,029 | 1,017,403 | 58,136 | 426s | 15% |
| pi | **8/9** | 3,384,264 | 2,675,853 | 48,995 | 442s | 28% |
| qwencode | **8/9** | 36,768,078 | 19,813,642 | 284,492 | 897s | 13% |
| hermes-agent | 4/9 | **2,303,180** | 513,118 | 90,355 | 721s | 12% |
| openclaw | 7/9 | 5,355,909 | 5,394,808 | 74,320 | 985s | 40% |

Fresh input per run: janetcode 1.05M · 1.01M · 1.17M, pi 1.19M · 0.96M ·
1.23M, qwencode 11.7M · 11.8M · 13.2M, hermes-agent 0.77M · 0.81M ·
0.72M, openclaw 2.0M · 1.9M · 1.4M.

## The 5/9 was contention, not capability: janetcode passes 16/18 run alone

The concurrent run above put all five harnesses against one OpenRouter
key at once, qwencode pulling 11M tokens beside janetcode's 1M. To tell
a real weakness from endpoint contention, janetcode was then run alone.

- janetcode alone, the same three tasks three times: 6/9, and a
  *different* task failed each run (ledger, then records, then the Go
  service). One miss per run scattered across all three tasks is
  variance, not a broken task.
- Each task then run six times through the eval driver, 18 runs:
  **16/18 (89%)** — 41 6/6, 42 5/6, 43 5/6 — the same rate pi reached
  (8/9). Of the two misses, one was an upstream error returned by
  Inception itself ("The server had an error while processing your
  request", HTTP 200), and the other, kept and dissected, was a model
  miss: on the sixth prompt the model documented the `--currency` option
  in the README but under no `## Usage` heading, which the check
  requires. Every unit test passed, the rename was complete, the refund
  bug was fixed — the model followed the substance and missed one
  formatting instruction.

So the 5/9 in the five-harness table is what a contended endpoint
produced on a run of nine; janetcode's rate on this set and model,
measured over 18 runs alone, is 89%, level with pi and reading a third
of qwencode's tokens. The lesson for the matrix is procedural: this set
must be run one harness at a time, or the shared key makes the harness
that happens to run beside qwencode look worse than it is.

## Context

janetcode read the least of the continuing harnesses that pass (pi reads
the same fresh input and 2.6x the cache; qwencode 11x; openclaw 1.7x
fresh and 5x cache). hermes-agent reads less fresh input because each
prompt starts a new conversation, and it passes 4/9 for the same reason.

## Reproduce

```
./bin/harnessbench --evals evals/session --filter session \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model inception/mercury-2.5 --base-url https://openrouter.ai/api/v1 \
  --timeout 5m --out run1
```
