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
