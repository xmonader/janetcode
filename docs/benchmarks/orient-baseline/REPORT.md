# Orient and baseline: results

Run 2026-10-02 per [PLAN.md](PLAN.md). Executor
`deepseek-ai/DeepSeek-V4.1-Flash` on the ai02 broker's self-hosted route
(gpu_octo), every eval under `--eval-gate`, 3 runs per task, one task at a
time. The provenance guard classified every run: no run was served by
another vendor (0 MISMATCH in every arm). The route dropped many requests
— almost all `504 no reply received over mycelium`, two rate limits — and
those runs are INFRA: left out of the pass rate and of the paired
comparison, never counted as failures.

## Experiment 2 — the pre-edit baseline (`verify_baseline`)

Arms: **A** the gate alone (`advisor_mode=off`, `plan`), **E** the same
plus `verify_baseline=true`.

| set | arm | scored / INFRA | pass | turns per solved | wall per solved | context per solved |
|---|---|---|---|---|---|---|
| hidden | A | 11 / 10 | 7/11 | 8.3 | 124s | 131k |
| | E | 11 / 10 | **11/11** | 9.7 | 119s | **71k** (×0.54) |
| gated | A | 5 / 4 | 5/5 | 33.4 | 200s | 539k |
| | E | 5 / 4 | 5/5 | 32.4 | 237s | 536k |
| hard | A | 12 / 6 | 12/12 | 4.2 | 22s | 21k |
| | E | 12 / 6 | 12/12 | 3.9 | 23s | 20k |

Where the difference is: the two held-out **controls**, whose checks fail
for a reason outside the repository (a credential the harness does not
hold, a service that is not listening).

| | 97 missing credential | 98 dead service | ordinary hidden tasks |
|---|---|---|---|
| A, gate | 0/3 | 0/1 | 7/7 |
| E, gate + baseline | 2/2 | 3/3 | 6/6 |

0/4 against 5/5 on the controls (Fisher's exact, one-sided, p ≈ 0.008).
Without a baseline the model spends its repair rounds on a failure no
edit can fix and the run is graded a failure; with one, the failure is
reported as pre-existing and the run ends. Nothing was lost on the tasks
that start red (`hard`, 12/12 both) — the case the plan named as the
risk — and none on the ordinary held-out tasks.

The per-set pass-rate interval from `--eval-compare` is wide (hidden: +50%
[0%, +100%] over 4 shared tasks) because INFRA drops left few shared
task-runs; the control result above is the one that clears significance.

**Decision** (the plan's rule: no pass-rate drop, and fewer rounds or less
cost per solved task): met — the pass rate rose on the controls and fell
nowhere, context per solved task fell on hidden (×0.54) and hard (×0.93)
and held on gated. `verify_baseline` is on by default from the commit
that cites this report. Cost: one extra run of the checks per turn, in the
background; the first edit waits for it (gated: 237s vs 200s per solved
task).

## Experiment 1 — orient vs plan vs nothing

Arms: **A** nothing (`advisor_mode=off`), **B** self-orientation (the
executor orients itself), **C** orientation by `Qwen/Qwen3.8-27B-FP8`,
**D** a plan by the same Qwen model (`advisor_mode=always`). No run in any
arm was served by another vendor.

| set | A nothing | B self-orient | C orient (Qwen) | D plan (Qwen) |
|---|---|---|---|---|
| hidden | 7/11 | 5/9 | 7/11 | 9/12 |
| — of which controls 97/98 | 0/4 | 0/4 | 0/4 | 1/3 |
| gated | 5/5 | 2/2 | 4/4 | 5/5 |
| hard | 12/12 | 12/12 | 13/13 | 12/12 |
| INFRA (left out) | 20 | 25 | 20 | 19 |

Every arm passed every scored run outside the two controls. On these sets
`DeepSeek-V4.1-Flash` already solves the ordinary tasks, so there is no
headroom for advice of any kind to show; the only failures are the
controls, which no advice fixes (they need the baseline, experiment 2).
Turns and context per solved task are within noise of each other (hard:
4.1–4.5 turns, 22–26k context).

**Decision** (the plan's rule: orient becomes the default only if C beats
D, self-orient is offered only if B beats A): neither holds. The default
turn-start style stays `plan` (consulted only under `advisor_mode
always`), and orient stays available behind `advisor_turn_start_style`.
This does not refute hero_agent's result — their +25 points came from
tasks their executor was failing; here there were none to win back. A
fair retest needs a set the executor fails on its own merits (a weaker
executor on `hard`, or harder held-out tasks).

What did come out of experiment 1's code is kept regardless: a turn-start
consult cut off at its token limit, or present only as thinking, is no
longer injected, and the budget is 4096 tokens so reasoning models are not
cut off by their own thinking.
