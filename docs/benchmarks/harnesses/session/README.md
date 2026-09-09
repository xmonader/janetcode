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
