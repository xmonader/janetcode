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

It is left in place rather than removed: models differ in whether they
keep a written plan, and one that does would use it. But nothing here
shows it helping, and the honest summary is that it is unproven on the
only model it has been measured against.

## What today's compaction and retrieval work did do

985k to 865k context across the three sessions, all still passing — 0.88x,
and 0.77x on the longest session, which is the one that compacts hardest.
Single runs, so treat the total as indicative and the direction as real.

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
