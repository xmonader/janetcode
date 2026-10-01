# Explore, then compile: does a compiled agent loop pay?

Branch `development_jit`, 2026-10-01. The idea: instead of the model
interpreting every step (one model call per tool call), it explores, then
hands the rest of the work over as one program that the harness runs
without model calls in between (`run_plan`), falling back to the model
only where a check fails.

**Verdict: most of the value is a prompt, not a compiler.** A 110-word
instruction to batch work cut qwen3.8-27b's cost per solved multistep task
by 73% and its wall-clock by 55% at the same pass rate. `run_plan` added
nothing over that prompt for qwen, and for deepseek-v4.1-flash it helped
only on fan-out tasks, with low adoption (19–37% of runs).

## 1. The opportunity, measured on recorded sessions

`depgraph.py` replays every recorded session (106 with real models, 640
model calls that made tool calls) and puts each call on a dependency
graph: a call depends on an earlier step when an argument of it contains a
token first seen in that step's results. The longest chain is the fewest
model calls a planner that knew the data flow would need.

| | avoidable model calls | input tokens saved |
|---|---|---|
| data dependencies only | 59% (67% of multi-step turns) | 73% |
| + "a command's output may steer the next call" | 36% (41%) | 49% |

Checked by hand on the largest turn (36 calls): four consecutive calls
each made one edit to the same file, all from one earlier read. The
conservative count (8) is close to a hand count (≈11); the data-only count
(4) is too optimistic, because test → fix loops depend on the error
message without quoting it. The corpus is dominated by qwen3.7-plus goal
runs; the two Claude Sonnet models wasted least (25–27%).

## 2. The A/B/C

Arms, each 3 runs per task, through the ai02 broker:

- **A** baseline.
- **B** `prompt-batch.md` appended: explore, then make every known change
  and run the checks in the same response.
- **C** `prompt-jit.md` appended and `run_plan` offered (`JANETCODE_JIT=1`).
  The prompts are the same length and differ only in the mechanism.

Sets: `multistep` (12 tasks that need planning and reading results back)
and `fanout` (9 tasks applying one change across many files).

| model | set | arm | pass | model calls | context in | out | $/solved | wall |
|---|---|---|---|---|---|---|---|---|
| qwen3.8-27b | multistep | A | 35/36 | 10.6 | 117k | 10.6k | 0.0489 | 148s |
| | | B | 36/36 | **6.3** | **43k** | **2.7k** | **0.0133** | **66s** |
| | | C | 36/36 | 6.8 | 52k | 2.7k | 0.0141 | 68s |
| | fanout | A | 27/27 | 4.0 | 19k | 0.6k | 0.0038 | 18s |
| | | B | 27/27 | 4.5 | 23k | 0.7k | 0.0048 | 23s |
| | | C | 27/27 | 3.7 | 20k | 0.6k | 0.0042 | 17s |
| deepseek-v4.1-flash | multistep | A | 35/36 | 6.1 | 39k | 3.4k | 0.0051 | 19s |
| | | B | 35/36 | 5.8 | 38k | 3.9k | 0.0054 | 22s |
| | | C | 36/36 | 5.9 | 43k | 3.9k | 0.0057 | 24s |
| | fanout | A | 27/27 | 4.9 | 27k | 0.8k | 0.0020 | 11s |
| | | B | 27/27 | 5.0 | 28k | 1.0k | 0.0021 | 13s |
| | | C | 27/27 | **3.7** | **21k** | 0.7k | **0.0009** | 14s |

Per task, qwen's B gain is broad, not one outlier: model calls per run went
from e.g. 6/8/6 → 4/4/4 (51), 11/6/8 → 4/4/4 (54), 27/48/28 → 14/4/4 (61).
One B run of 62 took 36 calls. No pass-rate difference is significant at
the corrected 0.0125.

## What it means

- **The waste is a model habit, and a prompt fixes it where it exists.**
  qwen3.8-27b interprets one step at a time unless told otherwise; told,
  it batches, and the savings are large. deepseek-v4.1-flash already
  batches, so neither the prompt nor the tool moves it on multistep work.
- **`run_plan` is not worth shipping on this evidence.** Over the batching
  prompt it changed nothing for qwen (C vs B: +$0.0008/solved, context
  ×1.19). Its one win — deepseek fan-out, $0.0020 → $0.0009 — rests on 27
  runs with the tool used in 10 of them, and wall-clock got worse, not
  better. The model reaches for `execute_code` instead, which already is a
  program that calls tools.
- **The safety half of the idea is untested.** A whole-plan check needs a
  plan the model actually writes; at 19–37% adoption there is not one to
  check most of the time.

## Next

1. Make the batching guidance part of the default prompt, gated by
   measurement: rerun A vs B on qwen3.7-plus (the model most of the recorded
   sessions use), mimo-v2.6-flash and the `hard` set, and ship only if no
   pass rate drops.
2. Leave `run_plan` on this branch, off by default.

## Reproduce

```sh
python3 docs/benchmarks/jit/depgraph.py ~/.local/share/janetcode/sessions/*.jsonl
# per arm (see the table for flags):
JANETCODE_JIT=1 janetcode --provider openai --model qwen/qwen3.8-27b \
  --append-system-prompt @docs/benchmarks/jit/prompt-jit.md \
  --eval evals --eval-only multistep --eval-runs 3 --eval-json C.json
```

`results.json` holds every run's numbers (name, pass, model calls, tokens,
cost, time). The first arm-C run of deepseek is not in it: the eval agent
did not offer `run_plan` then (fixed in the next commit), so it measured a
prompt naming a missing tool.
