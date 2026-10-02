# Orient and baseline: the A/B to run

Two ideas from hero_agent, built behind switches that default to today's
behaviour. This plan is what to run before either default changes. Both
switches are config keys, so every arm is the same binary with `--set`.

| switch | values | what it changes |
|---|---|---|
| `advisor_turn_start_style` | `plan` (default), `orient` | what the turn-start consult asks for; orient reads the likeliest files and answers LOOK / WATCH FOR / CHECK, never the edit, and runs without `advisor_mode` |
| `verify_baseline` | `false` (default), `true` | the gate compares with the checks run before the first change; new failures go back on the usual rounds, pre-existing ones get one reminder |

Also in this change and not switchable (a plain fix): a turn-start reply
that hit the token limit or exists only as thinking is dropped. It only
fires when the forced advisor is on, so arm A below is unaffected.

## Before any run

1. **Provider check.** Run only on routes that serve the named model, and
   confirm it per arm (the ai02 broker answered every model name with Kimi
   on 2026-10-01). Until the eval runner records the serving provider
   itself, check each results file: no run may have a tool-call id
   starting `tool_` (Kimi's format).
2. **Infrastructure failures** (HTTP 429/5xx, "no reply over mycelium")
   are excluded from both numerator and denominator, per arm, and the
   comparison is paired by task over the runs both arms completed.
3. Run arms of one model in sequence, one task at a time (`--eval-jobs 1`)
   on the self-hosted routes; they drop requests under concurrency.

## Experiment 1 — orient vs plan vs nothing

Question: does pointing the executor at evidence before it starts raise
the pass rate, and does it beat handing it a plan?

| arm | flags |
|---|---|
| A none | `--set advisor_mode=off` |
| B self-orient | `--set advisor_turn_start_style=orient --set advisor_model= --set strong_model=` |
| C orient by a stronger model | `--set advisor_turn_start_style=orient --set advisor_model=<strong>` |
| D plan by the same stronger model | `--set advisor_mode=always --set advisor_model=<strong>` |

Eval runs load the user's config file, and orient uses `advisor_model`,
then `strong_model`, before the session's own model: the smoke run below
was oriented by a `z-ai/glm-5.3` advisor from the config, not by the
executor. So every arm sets the slots explicitly, and B clears both.

B is the cheap version hero_agent did not measure: the same model orients
itself. C vs D is hero_agent's own comparison (6/10 vs 4/10 on their
probe), on janetcode's tasks. D's turn-start reply is now dropped when
truncated, which hero_agent found was the failure mode of a plan.

- Executor: `deepseek-ai/DeepSeek-V4.1-Flash`; a second executor
  `XiaomiMiMo/MiMo-V2.6-Flash-RL` if the first shows a difference.
- Stronger model for C/D: `Qwen/Qwen3.8-27B-FP8`, or any route that passes
  the provider check and is stronger on `evals/hard` alone.
- Sets: `evals/hidden` (held-out grading tests, the set where a model
  cannot tune to the visible checks), `evals/gated` and `evals/hard`.
  Always with `--eval-gate`, so the orient prompt names the real checks.
- Runs: 3 per task per arm (hidden 7 tasks, gated 3, hard 6: 48 runs per
  arm). Report pass rate with the bootstrap interval from
  `--eval-compare`, cost and model calls per solved task.

```sh
J="janetcode --provider openai --model deepseek-ai/DeepSeek-V4.1-Flash --eval-gate --eval-runs 3 --eval-jobs 1"
for set in hidden gated hard; do
  $J --eval evals/$set --set advisor_mode=off --eval-label A --eval-json orient-$set-A.json
  $J --eval evals/$set --set advisor_turn_start_style=orient --set advisor_model= --set strong_model= --eval-label B --eval-json orient-$set-B.json
  $J --eval evals/$set --set advisor_turn_start_style=orient --set advisor_model=Qwen/Qwen3.8-27B-FP8 --eval-label C --eval-json orient-$set-C.json
  $J --eval evals/$set --set advisor_mode=always --set advisor_model=Qwen/Qwen3.8-27B-FP8 --eval-label D --eval-json orient-$set-D.json
done
janetcode --eval-report orient-hidden-B.json --eval-compare orient-hidden-A.json   # and C/A, C/D
```

Decide: orient becomes the default turn-start style if C beats D, and
self-orient (B) becomes worth offering on its own if B beats A, each at a
corrected significance on the pooled sets, without a cost per solved task
more than 1.3x.

## Experiment 2 — baseline

Question: does knowing what was already broken save repair rounds
without losing tasks?

Most janetcode eval tasks *start* red (the request is to make failing
tests pass), so on them the baseline's effect is the reminder replacing
up to three repair rounds. That is the risky direction: a model that
stops after the reminder on a half-fixed task loses it. The control sets
are where the benefit should show: a task whose checks were red for an
unrelated reason.

| arm | flags |
|---|---|
| A gate | `--eval-gate --set advisor_mode=off --set advisor_turn_start_style=plan` |
| B gate + baseline | the same plus `--set verify_baseline=true` |

(The advisor switches are pinned so the user's config cannot add consults
to one arm or the other.)

- Sets: `evals/hidden` (including controls 97 and 98, whose gate fails
  for a reason outside the repository: the case the baseline is for),
  `evals/gated`, `evals/hard`.
- Executor: `deepseek-ai/DeepSeek-V4.1-Flash`. 3 runs per task per arm.
- Measure: pass rate (must not drop), repair rounds per task (count
  `sending the model back` notices in the session logs, or `turns`),
  cost per solved task, wall-clock (the first edit now waits for the
  baseline run).

Decide: default on only if the pass rate is unchanged within the interval
and repair rounds or cost per solved task drop. If the pass rate drops on
tasks that start red, the reminder is too weak for them: try making it a
repair round when the request names a failing test, and measure again.

## Smoke run (2026-10-02, before merge)

One task, both switches on, `deepseek-ai/DeepSeek-V4.1-Flash` on ai02
(tool-call ids `chatcmpl-tool-…`, not Kimi's), `evals/hidden`
`91-hidden-csvsum`, `--eval-gate`: passed in 29 s, 5 tool calls, $0.0019.
The orientation was injected (LOOK named `Sum` in `sum.go` and the
whitespace-line, line-numbering and short-row cases; WATCH FOR named the
existing test) — by the config's glm-5.3 advisor, which is why the arms
above clear the slots. The baseline finished before the first edit, so
the edit did not wait; the gate passed. A first attempt with a 1200-token
budget had its orientation discarded as cut off at the limit (the
reasoning model's thinking counts against it), which is why the
turn-start budget is 4096.

## Afterwards

Write the numbers into `docs/benchmarks/orient-baseline/REPORT.md`, with
the provider check and the infrastructure exclusions per arm, and change
the defaults (or not) in a separate commit that cites it.
