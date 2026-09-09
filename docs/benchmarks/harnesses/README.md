# Harness benchmarks (evals 01–20)

This directory holds raw per-harness results and a comparative report for
janetcode eval tasks `01-create-file` through `20-env-default`, run in four
batches of five against model `qwen3.7-plus` at the Alibaba coding
OpenAI-compatible endpoint.

## Layout

| file | contents |
|------|----------|
| `REPORT.md` | Comparative tables: input / cache / output tokens, wall time, pass/fail |
| `NOTES.md` | How each CLI was launched (no secrets) |
| `combined.json` | All harness results |
| `<harness>.json` | One file per harness (`pi`, `janetcode`, `qwencode`, `hermes-agent`, `openclaw`, `grok`) |

A harness that cannot target the endpoint still has a JSON file with twenty
task rows marked `unavailable` and the captured failure.

Token fields the harness did not emit are `"set": false` (`n/a` in the
report), never estimated.

## Re-run

From the repo root, with the key in the process environment only:

```sh
export DASHSCOPE_API_KEY=...          # or OPENAI_API_KEY
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
make build
go test ./internal/eval -count=1 -run 'TestBatchesOfFive|TestAggregate'
go build -o bin/harnessbench ./cmd/harnessbench
./bin/harnessbench --evals evals --out docs/benchmarks/harnesses --janetcode bin/janetcode
```

Do not put the key in files, git remotes, or this directory.
