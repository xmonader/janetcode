# Session benchmark (evals 41–43)

Multi-prompt sessions — each prompt is one non-interactive invocation
continuing the harness's own session in the same tree (`pi --session-id`,
`qwen --session-id`/`--resume`, `grok --session-id`/`--resume`; janetcode
keeps the agent). Later prompts depend on earlier ones, so a harness that
lost the thread fails the final check. `REPORT.md` has the totals and, per
task, the **context growth** table: what each harness read per prompt.

Re-run from the repo root, key in the environment only:

```sh
export OPENAI_API_KEY=...
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
make build && go build -o bin/harnessbench ./cmd/harnessbench
./bin/harnessbench --evals evals/session --filter session --out docs/benchmarks/harnesses/session --timeout 5m
```

## Does the plan tool earn its place? (measured 2026-09-09)

The todo tool was built on the reasoning that a long task drifts and a
written plan holds it together. Measured on the session set with
qwen3.7-plus, that claim is not supported:

| run | context read | passed | todo calls |
|---|---:|---:|---:|
| baseline (before today's work) | 985k | 3/3 | tool absent |
| today's compaction and retrieval work | 865k | 3/3 | tool absent |
| with the tool in the agent | 897k | 3/3 | **0** |
| with the tool and a prompt line telling the model to use it | — | 1/1 | **0** |

The model never called it once, in three sessions of six and seven
dependent prompts each — exactly the shape the tool was built for — with
and without an instruction in the system prompt naming it.

What can be claimed: the tool is unused by this model. What cannot: that
it costs 4%. The 865k and 897k runs are one run each, and identical
configurations have swung 15% between runs on this set, so the difference
is inside the noise.

**Update, with more runs and a second model.** The eval runner now reports
tool adoption from the runs' event logs, so this is countable rather than
anecdotal:

| runs | model | tasks | `todo` calls |
|---:|---|---|---:|
| 9 | qwen3.7-plus | session ×3 (6–7 dependent prompts each) | 0 |
| 15 | claude-sonnet-4.5 | fanout ×3, `execute_code` on | 0 |
| 15 | claude-sonnet-4.5 | fanout ×3, `execute_code` off | 0 |
| 15 | claude-sonnet-4.5 | fanout ×3, after the stub fix | 0 |

Zero calls in 54 runs across two models, counted from the saved event
logs, and the multi-prompt session shape the tool was built for is nine of
them. The two earlier single-run qwen sessions — one with a system-prompt
line naming the tool — were also zero, by hand. `repo_map` and `task` were never
called in these runs either. `expand_output` was not either, but that one
is correct rather than a defect: those runs compacted zero times, so there
was nothing archived for it to fetch.

**The tool was not the problem; the instruction was.** Asked directly, the
tool works: qwen3.7-plus writes a plan, marks a step in progress and marks
it done. So the model was seeing it and declining it, 54 times.

The trigger is why. Both the system prompt and the tool's own description
said to use it "when a job has several parts that are easy to lose track
of", and both then spent a clause discouraging it ("skip it for a single
edit", "not for a single edit"). A model asked whether it might lose track
answers no, every time. The judgement was subjective and it was always
resolved the same way.

Both now use a countable trigger instead — three or more distinct steps, or
the user listing several things — with the instruction to count rather than
to judge, and to write the plan before starting.

| | `todo` calls |
|---|---|
| before, qwen3.7-plus, 9 session runs | 0 |
| after, a request with three explicit parts | 4 (plan first, then marked off as it went) |
| after, qwen3.7-plus, 6 session runs | 3, in 1 of 6 runs |
| after, qwen3.7-plus, 9 fanout runs | 0 |

The last row is the honest limit of the fix. One of those nine tasks
(`59-three-part-request`) asks for three things in one prompt, which is
precisely the trigger, and the model did not write a plan — while the same
model on a similar three-part request did. Adoption went from never to
sometimes, not from never to reliably.

One in six looks low until you read the prompts: each step of a session
task asks for a single thing ("add a Timeout field", "make the server use
it"), so under a three-step rule most correctly do not warrant a plan. The
run that did use it was the one prompt that asked for four things, and it
planned all four. That is the rule working, not failing.

What is still not measured is whether keeping the plan makes any of these
runs *better*. Adoption is not benefit, and 6/6 passed either way.

Check any of this against saved results without paying for new runs:

```
janetcode --eval-report <results.json>
```

## What today's compaction and retrieval work did do

985k to 865k context across the three sessions, all still passing — 0.88x,
and 0.77x on the longest session, which is the one that compacts hardest.

That is one run of each configuration, and the next section measures the
run-to-run spread of this set at 21%. A 12% difference is inside it. The
direction is not established either: a 0.88x that could as easily have been
1.05x says nothing about which way the work moved the number. What can be
said is that the set still passes 9/9 and that nothing here shows a
regression.

`--eval-report <file> --eval-compare <base>` now re-reads saved results and
prints the paired bootstrap over tasks, so a claim like this one can be
checked against its own data without paying for another run. Applied to the
compaction ablation pair in `docs/benchmarks/compaction`, a 17% context
difference comes back as x1.17 [0.92, 1.76] — inside the noise. Any future
claim on this set has to clear that interval before it is a claim.

## How noisy is this benchmark? (three runs of each task)

Every comparison above this line was a single run per configuration.
Running the set three times says how much that is worth:

| task | run 1 | run 2 | run 3 | spread | passed |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 358k | 366k | 388k | 8% | 3/3 |
| 42-session-records | 242k | 113k | 143k | **113%** | 3/3 |
| 43-session-python-ledger | 339k | 294k | 311k | 15% | 3/3 |
| **whole set** | **940k** | **774k** | **843k** | **21%** | **9/9** |

The pass rate is stable — 9/9. The token cost is not: the set swings 21%
between identical runs, and the search-heavy task swings by more than it
costs, because how widely the model searches a large tree varies from run
to run.

**So single-run comparisons on this benchmark are not evidence**, and the
ones above should be read as indicative only. The mean of three runs is
853k against a single-run baseline of 985k; every run came in under the
baseline, which is suggestive, but with n=1 on the baseline side it is not
a measured improvement and is not claimed as one.

Anything reported from this set should be at least three runs
(`--eval-runs 3`), and a difference under about 20% needs more than that.
