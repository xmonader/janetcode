# A task set with headroom, and what it says about four harness features

- Model: `qwen/qwen3.7-flash` on OpenRouter (`https://openrouter.ai/api/v1`),
  janetcode's own eval runner, four tasks at a time, a private HOME so no
  operator config, memory, skills or MCP is read
- Tasks: `evals/` 51–62, tagged `multistep`, three runs each; 45–60 tool
  calls allowed per task and a ten-minute budget, so neither limit
  decides a result
- Recorded 2026-09-11. Raw `--eval-json` files are beside this one:
  `calibration-presharpen.json`, `baseline.json`, `off-todo.json`,
  `off-coach-syntax.json`, `off-repomap.json`, `off-verify.json`,
  `plus-one-run.json`. Their per-request event streams are stripped and
  outputs over 1200 bytes truncated — everything `--eval-report` and
  `--eval-compare` read is intact, and both were re-run against the
  trimmed files to confirm it.

## Why the set exists

The easy set (01–20) passes 60/60 and the hard set (21–26) 87/90 across
five harnesses on this model family. On a set everything passes, a
feature worth nothing and a feature worth a lot both score 100%, so the
leanness audit of 2026-09-11 had to leave two prompt cuts undecided: the
todo bullet's ~1.3 KB and the task/execute_code duplication. This set is
twelve single prompts built so that a model gets them right only by
planning, running something and reading the result back.

## Calibration

Two passes. The first exposed six tasks the model passed three times out
of three; those were sharpened (commit `afab1ca`) and the second pass is
the baseline the ablations compare against.

Pass 1, before sharpening — 28/36 (78%):

| task | runs | pass | tool calls per run | fresh in | cache read | out | wall |
|---|---|---:|---|---:|---:|---:|---:|
| 51-price-chain-bug | .PP | 2/3 | 8/21/11 | 28,690 | 191,145 | 16,414 | 340s |
| 52-options-refactor | P.P | 2/3 | 21/8/22 | 33,119 | 218,137 | 14,839 | 174s |
| 53-tally-cli | PPP | 3/3 | 13/9/6 | 23,613 | 89,096 | 7,417 | 89s |
| 54-csv-clean | PPP | 3/3 | 8/4/5 | 23,396 | 90,309 | 12,270 | 156s |
| 55-config-provenance | PPP | 3/3 | 15/19/23 | 26,350 | 179,307 | 12,882 | 161s |
| 56-two-failures | PP. | 2/3 | 20/9/10 | 25,351 | 118,949 | 5,810 | 93s |
| 57-expr-layers | PP. | 2/3 | 30/15/16 | 51,101 | 478,725 | 33,546 | 524s |
| 58-width-migration | PPP | 3/3 | 7/7/15 | 23,461 | 121,517 | 8,429 | 99s |
| 59-memo-race | PPP | 3/3 | 6/10/7 | 32,497 | 133,141 | 7,899 | 136s |
| 60-fence-aware-trim | P.. | 1/3 | 15/7/7 | 26,039 | 121,837 | 8,853 | 109s |
| 61-js-semver | ..P | 1/3 | 15/13/7 | 63,537 | 302,355 | 29,790 | 284s |
| 62-env-schema-check | PPP | 3/3 | 13/8/6 | 26,950 | 126,757 | 14,623 | 163s |

Two of those eight failures were not the task. `52 #2` died on a
provider-side malformed tool call (`tool call todo has invalid JSON
arguments: {"todos": }`) and `57 #3` had its check killed at the
two-minute cap, because the check compiled the module twice with `go
run`. The second is fixed; the first is the endpoint and will recur.

The six tasks at 3/3 were sharpened: 53 reads stdin and is checked with
`--top 0` and an exclude list with empty items; 54's unseen second
fixture grew an escaped quote inside a quoted cell, a leading `+`, a
negative id, a too-long row and a duplicate differing only by trailing
space; 55 now fails if the scaling mechanism is deleted rather than
corrected; 58 is also run on a file with an empty cell and a quoted comma
and on one with only a header; 59 times eight distinct keys, so a single
mutex held across the computation — race-free, once per key — now fails,
verified directly against the old reference solution; 62 gained a fixture
where a value contains `=` and `#` and a line has no `=`.

Pass 2, the calibrated set — 32/36 (89%), and this is the ablation
baseline:

| task | runs | pass | tool calls per run | fresh in | cache read | out | wall |
|---|---|---:|---|---:|---:|---:|---:|
| 51-price-chain-bug | PPP | 3/3 | 24/17/9 | 36,582 | 268,396 | 15,545 | 174s |
| 52-options-refactor | PPP | 3/3 | 31/25/23 | 44,119 | 414,860 | 20,918 | 259s |
| 53-tally-cli | P.P | 2/3 | 14/50/18 | 54,644 | 721,868 | 22,182 | 277s |
| 54-csv-clean | P.P | 2/3 | 32/14/4 | 32,475 | 373,529 | 17,935 | 221s |
| 55-config-provenance | PPP | 3/3 | 15/20/21 | 30,502 | 182,053 | 13,638 | 156s |
| 56-two-failures | PPP | 3/3 | 12/12/11 | 23,938 | 122,438 | 4,979 | 64s |
| 57-expr-layers | PPP | 3/3 | 19/24/20 | 48,693 | 495,497 | 23,278 | 280s |
| 58-width-migration | PPP | 3/3 | 6/17/6 | 22,440 | 113,749 | 13,193 | 151s |
| 59-memo-race | PPP | 3/3 | 11/7/20 | 42,701 | 243,275 | 24,482 | 315s |
| 60-fence-aware-trim | P.P | 2/3 | 6/8/10 | 25,576 | 103,390 | 4,038 | 56s |
| 61-js-semver | P.P | 2/3 | 39/43/8 | 110,958 | 1,281,544 | 52,009 | 496s |
| 62-env-schema-check | PPP | 3/3 | 10/7/5 | 24,467 | 86,932 | 19,264 | 196s |

36 runs, 618 tool calls, 44 minutes, $0.063.

### The set is better than the old ones and still not good enough

The target was 40–85% per task. Over the six runs each task has now had,
60 and 61 sit at 3/6, 51/52/53/54/56/57 between 4/6 and 6/6, and
55/58/62 have never failed. Two things are visible in that:

- **Run-to-run noise on this set is as large as the effects.** 51 went
  2/3 then 3/3, 60 went 1/3 then 2/3, 53 went 3/3 then 2/3 — on tasks
  that did not change between the two passes, or changed in the direction
  opposite to the result. Three runs per task is not enough to place a
  task in a 45-point band; it is barely enough to tell 0/3 from 3/3.
- **89% leaves 11 points of headroom, where 100% left none.** That is the
  improvement over the old sets, and it is real but modest. Four tasks do
  the discriminating; eight are ballast.

The honest reading is that this set is a better instrument than 01–26 and
still a weak one. What would fix it is more runs per task rather than
more tasks: at five runs the interval on a single task halves.

### The stronger model saturates it

One run on `qwen/qwen3.7-plus`, same endpoint and harness: **12/12**, 114
tool calls, 11 minutes, $0.107 — six times the flash bill for a third of
the work.

| task | 3.7-plus tool calls | 3.7-flash mean tool calls |
|---|---:|---:|
| 51 | 11 | 17 |
| 52 | 15 | 26 |
| 53 | 12 | 27 |
| 54 | 3 | 17 |
| 55 | 21 | 19 |
| 56 | 9 | 12 |
| 57 | 8 | 21 |
| 58 | 8 | 10 |
| 59 | 7 | 13 |
| 60 | 9 | 8 |
| 61 | 6 | 30 |
| 62 | 5 | 7 |

The spread is what the set was built to show: plus does the same work in
roughly half the calls and never flails. But 12/12 means the set has no
headroom on plus at all, so **an ablation on plus would measure nothing**.
Everything below is a statement about flash.

## The ablations

Each ran `--eval-runs 3 --eval-off <switch> --eval-compare baseline.json`
against the same baseline, one batch at a time.

| variant | passed | pass-rate delta [95%] | verdict |
|---|---|---|---|
| baseline | 32/36 (89%) | — | — |
| `--eval-off todo` | 28/36 (78%) | −11.1% [−30.6%, +5.6%] | not significant |
| `--eval-off coach,syntax` | 30/36 (83%) | −5.6% [−16.7%, +5.6%] | not significant |
| `--eval-off repomap` | 31/36 (86%) | −2.8% [−19.4%, +13.9%] | not significant |
| `--eval-off verify` | 30/36 (83%) | −5.6% [−22.2%, +8.3%] | not significant |

Every interval includes zero. On pass rate, **no feature shows a
measurable effect on this set at three runs**. Every ablation points the
same way — down — which is suggestive and is not evidence; four coin
flips landing heads is not a biased coin.

Per task, so the noise is visible rather than hidden in a total:

| task | baseline | off todo | off coach,syntax | off repomap | off verify |
|---|---|---|---|---|---|
| 51-price-chain-bug | 3/3 | 3/3 | 3/3 | 3/3 | 2/3 |
| 52-options-refactor | 3/3 | 2/3 | 2/3 | 2/3 | 3/3 |
| 53-tally-cli | 2/3 | 3/3 | 2/3 | 3/3 | 3/3 |
| 54-csv-clean | 2/3 | 3/3 | 3/3 | 3/3 | 3/3 |
| 55-config-provenance | 3/3 | 3/3 | 3/3 | 3/3 | 3/3 |
| 56-two-failures | 3/3 | 3/3 | 3/3 | 3/3 | 3/3 |
| 57-expr-layers | 3/3 | 1/3 | 3/3 | 2/3 | 2/3 |
| 58-width-migration | 3/3 | 3/3 | 3/3 | 3/3 | 3/3 |
| 59-memo-race | 3/3 | 2/3 | 2/3 | 1/3 | 1/3 |
| 60-fence-aware-trim | 2/3 | 2/3 | 2/3 | 3/3 | 2/3 |
| 61-js-semver | 2/3 | 2/3 | 1/3 | 2/3 | 2/3 |
| 62-env-schema-check | 3/3 | 1/3 | 3/3 | 3/3 | 3/3 |
| **total** | **32/36** | **28/36** | **30/36** | **31/36** | **30/36** |

59 drops in all four ablations, including two that should have nothing to
do with it, which is what a noisy task looks like, not a finding.

### What did clear zero: context and cost

The same paired bootstrap on the resource columns is more informative
than the pass rate, because every task contributes a number rather than a
bit:

| variant | context per solved | paired [95%] | cost per solved | paired [95%] |
|---|---|---|---|---|
| `off todo` | 153k → 98k (×0.64) | **×0.56 [0.39, 0.89]** | $0.00198 → $0.00168 | ×0.74 [0.55, 1.04] |
| `off coach,syntax` | 153k → 151k (×0.99) | ×0.92 [0.56, 1.34] | $0.00198 → $0.00200 | ×0.95 [0.70, 1.27] |
| `off repomap` | 153k → 126k (×0.82) | ×0.80 [0.52, 1.44] | $0.00198 → $0.00209 | ×1.02 [0.64, 1.76] |
| `off verify` | 153k → 123k (×0.80) | **×0.75 [0.60, 0.94]** | $0.00198 → $0.00177 | **×0.84 [0.74, 0.97]** |

Two intervals exclude 1: turning the todo tool off cuts context per
solved task to 0.56 of baseline, and turning the verify gate off cuts it
to 0.75 and cost to 0.84. Both buy that saving at a pass rate that is
lower but not measurably so. So the data says these two features **cost
something real for a benefit this instrument cannot resolve** — which is
the shape of an open question, not of a verdict.

## The two prompt duplications the audit left open

### The todo bullet (~1.3 KB): keep, and this run does not settle it

Read the switch first. `--eval-off todo` removes the `todo` **tool** from
the eval agent (`cmd/janetcode/main.go`, `if !off["todo"]`); the system
prompt is static text and keeps its bullet either way. So this ablation
measures the todo feature as a whole, not the bullet's bytes, and it
cannot by construction answer "is the bullet worth 1.3 KB" — only "is
having todo at all worth anything here".

What it measures: −11.1% [−30.6%, +5.6%] on pass rate, and context per
solved at ×0.56 [0.39, 0.89]. The tool was reached for in 10 of 36
baseline runs (28%), 43 calls. So it is used, it is expensive, and its
benefit is not resolvable at this power.

Recommendation: **keep, and do not cite this run as support either way.**
Two concrete reasons not to cut on this evidence: the −11% point estimate
is the largest of the four and points the wrong way for a cut, and 62
went 3/3 → 1/3 with todo off, which is either signal or noise and three
runs cannot say. If the owner wants this settled, the experiment is a
prompt A/B — `JANETCODE_SYSTEM_MD` with the bullet removed and the tool
left in place — at five runs, which isolates the 1.3 KB rather than the
feature.

### The task/execute_code duplication: cut is defensible, on other grounds

There is no switch for this pairing, so it was not ablated. What this run
adds is adoption data on a set that is exactly the kind of work the
duplicated instruction is aimed at — multi-step, loop-shaped,
read-several-and-keep-two:

- `execute_code` was called in **4 of 36 baseline runs (11%), 8 calls**
- `task` is not in the eval agent's tool set at all
- `repo_map` was called once in 36 runs; `grep` and `expand_output` never

On twelve tasks explicitly built to reward scripting a loop, the model
scripted a loop in one run in nine. Two prompt bullets competing to
explain when to delegate and when to script are not being read as either.
That is an argument for cutting the duplication down to one instruction —
but it is an argument from adoption and prompt budget, not from a
measured pass-rate effect, and it should be labelled as such.

## What the owner has to decide

1. **More runs, or accept the noise.** Every conclusion above is limited
   by three runs per task. Five runs on this set costs about $0.11 and 75
   minutes per variant; the full five-variant series would be about $0.55
   and six hours of wall time. The alternative is to stop claiming
   pass-rate effects on this model and report only the context and cost
   columns, which already resolve.
2. **Whether to run the ablations on a model that does not saturate.**
   3.7-plus is 12/12 here, so the set cannot measure a harness change on
   it. Either the set gains a harder tier for plus, or ablations stay on
   flash and are labelled as such.
3. **The todo bullet's fate needs its own A/B**, not this ablation — the
   switch moves the tool, not the text.

## Reproduce

```sh
OPENAI_API_KEY=$OPENROUTER_API_KEY \
OPENAI_BASE_URL=https://openrouter.ai/api/v1 \
./bin/janetcode --provider openai --model qwen/qwen3.7-flash \
  --eval evals --eval-only multistep --eval-runs 3 --eval-jobs 4 \
  --eval-label baseline --eval-json baseline.json

./bin/janetcode --provider openai --model qwen/qwen3.7-flash \
  --eval evals --eval-only multistep --eval-runs 3 --eval-jobs 4 \
  --eval-off todo --eval-label off-todo \
  --eval-compare baseline.json --eval-json off-todo.json
```

Re-reading what is already here costs nothing:

```sh
./bin/janetcode --eval-report off-verify.json --eval-compare baseline.json
```
