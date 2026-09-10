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

## Three-run series

Each of these directories holds `run1/`–`run3/` of raw per-harness JSON
and a hand-written `REPORT.md` over the three; five harnesses
(janetcode, pi, hermes-agent, qwencode, openclaw), evals 01–20, one
endpoint per series.

| directory | model | endpoint |
|---|---|---|
| `qwen-five/` | `qwen3.7-plus` | DashScope |
| `qwen36-five/` | `qwen3.6-plus` | DashScope |
| `mercury-five/` | `inception/mercury-2.5` | OpenRouter |
| `deepseek-five/` | `deepseek/deepseek-v4-pro-0813` | OpenRouter |
| `qwen38flash-five/` | `qwen/qwen3.8-flash` | OpenRouter |
| `qwen37flash-five/` | `qwen/qwen3.7-flash` | OpenRouter |
| `glm53flash-five/` | `z-ai/glm-5.3-flash` | OpenRouter |
| `laguna-five/` | `poolside/laguna-s-2.1` | OpenRouter |
| `ling-five/` | `inclusionai/ling-3.0-flash` | OpenRouter |
| `gemma4-five/` | `google/gemma-4-31b-it` | OpenRouter |
| `mimo-five/` | `xiaomi/mimo-v2.5` | OpenRouter |
| `qwen-hard/` | `qwen3.7-plus`, tasks 21–26 (the hard set) | DashScope |
| `qwen-session/` | `qwen3.7-plus`, sessions 41–43, five harnesses, two runs | DashScope |

`eleven-models/REPORT.md` puts them side by side: tokens in, cached, out,
and cost at list price.

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
