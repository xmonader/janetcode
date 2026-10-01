# Explore, then compile: does a compiled agent loop pay?

Branch `development_jit`, 2026-10-01. The idea: instead of the model
interpreting every step (one model call per tool call), it explores, then
hands the rest of the work over as one program that the harness runs
without model calls in between (`run_plan`), falling back to the model
only where a check fails.

**Verdict, corrected the same evening: inconclusive, and the headline
result in the first version of this report was wrong.** The ai02 broker
began serving Kimi (Moonshot AI) for *every* model name at about
21:52 CEST on 2026-10-01, in the middle of these runs: responses carry
`x_aibroker.provider: "kimi"`, tool-call ids switch from the providers'
own formats (`call_…`, `toolu_…`) to Kimi's `tool_…`, and every model
answers "I'm Kimi". Classifying each run by its tool-call ids:

| arms | served by |
|---|---|
| deepseek multistep A, B, C | deepseek (all 36 runs each) |
| deepseek fanout A, B | deepseek |
| deepseek fanout C | **mixed**: 12 deepseek, 15 Kimi |
| qwen3.8-27b multistep A | qwen (33), Kimi (3) |
| qwen3.8-27b multistep B, C; fanout A, B, C | **Kimi** (all runs) |

So the "73% cheaper with a batching prompt" result compared qwen (A)
against Kimi (B): it measured the model swap, not the prompt, and is
withdrawn. So is the deepseek fan-out `run_plan` result. What survives is
one clean comparison — deepseek-v4.1-flash on multistep — where neither
the prompt nor `run_plan` changed anything (table below). The qwen and
fan-out-C rows are kept for the record, marked, and must not be read as
evidence.

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
| | | B ⚠ Kimi | 36/36 | 6.3 | 43k | 2.7k | 0.0133 | 66s |
| | | C ⚠ Kimi | 36/36 | 6.8 | 52k | 2.7k | 0.0141 | 68s |
| | fanout | A ⚠ Kimi | 27/27 | 4.0 | 19k | 0.6k | 0.0038 | 18s |
| | | B ⚠ Kimi | 27/27 | 4.5 | 23k | 0.7k | 0.0048 | 23s |
| | | C ⚠ Kimi | 27/27 | 3.7 | 20k | 0.6k | 0.0042 | 17s |
| deepseek-v4.1-flash | multistep | A | 35/36 | 6.1 | 39k | 3.4k | 0.0051 | 19s |
| | | B | 35/36 | 5.8 | 38k | 3.9k | 0.0054 | 22s |
| | | C | 36/36 | 5.9 | 43k | 3.9k | 0.0057 | 24s |
| | fanout | A | 27/27 | 4.9 | 27k | 0.8k | 0.0020 | 11s |
| | | B | 27/27 | 5.0 | 28k | 1.0k | 0.0021 | 13s |
| | | C ⚠ mixed | 27/27 | 3.7 | 21k | 0.7k | 0.0009 | 14s |

## 3. Rerun on routes that serve the named models (2026-10-02)

The broker's self-hosted routes `XiaomiMiMo/MiMo-V2.6-Flash-RL` (gpu_octo)
and `Qwen/Qwen3.8-27B-FP8` (gpu_vast) answer as MiMo and Qwen and issue
their own tool-call ids; every run below was checked and none was served
by Kimi. Multistep set, 2 runs per task, one task at a time. Both routes
dropped many requests (HTTP 429, 504 "no reply over mycelium"); those runs
are excluded, and every run that reached the model passed.

| model | arm | usable / 24 | pass | model calls | input | wall |
|---|---|---|---|---|---|---|
| MiMo-V2.6-Flash | A | 15 | 15/15 | 5.9 | 35k | 55s |
| | B | 15 | 15/15 | 5.1 | 29k | 48s |
| | C | 13 | 13/13 | 5.9 | 37k | 52s |
| Qwen3.8-27B | A | 10 | 10/10 | 7.0 | — | — |
| | B | 8 | 8/8 | 6.9 | — | — |
| | C | — | stopped at the 2-hour limit | | | |

Paired by task over the tasks both arms completed, MiMo: B/A model calls
0.89 [0.78, 1.02], input tokens 0.88 [0.76, 1.05] (8 tasks); C/A model
calls 1.15 [0.85, 1.35], input tokens 1.31 [0.92, 1.58] (6 tasks).
`run_plan` was used in 1 of 24 MiMo runs.

**Conclusion across the clean data (deepseek, MiMo, Qwen):** `run_plan`
does not pay — the models barely use it and it reduces nothing. The
batching prompt is at most a modest saving (about 10%, interval includes
none) on models that already batch. The case the idea was for — a model
with the one-step-at-a-time habit seen in the recorded qwen3.7-plus
sessions — was never tested on a model that has it.

## What it meant before the rerun

- On the one clean comparison (deepseek-v4.1-flash, multistep) the
  compiled loop and the batching prompt both did nothing: 6.1 → 5.8 → 5.9
  model calls, cost flat. deepseek already batches on its own.
- Whether a prompt or `run_plan` helps a model that does *not* batch —
  the qwen3.7-plus habit the session analysis found — is untested. That
  was the whole point of the qwen arms.
- `run_plan` adoption was low (about a third of deepseek runs), and the
  safety half of the idea (checking a whole plan) is untested.

## Next

1. Before any further live run, check the provider fingerprint (response
   `x_aibroker.provider`, the tool-call id prefix) and abort when it is
   not the model asked for. An eval should record the serving provider
   per call so this cannot pass unnoticed again.
2. Rerun qwen A/B/C, and deepseek fan-out C, once the broker serves the
   named models again — or against the providers directly.

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
