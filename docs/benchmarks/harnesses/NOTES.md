# Harness invocation notes

Model `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`. Key supplied via process environment only.

## janetcode

Native `--eval` in four batches of five, rebuilt and re-run after all cache-prefix
work: DashScope header + breakpoints, cwd/date split out of the cached system
block, last-tool marker, and never mutating sent tool-result messages.

Latest totals: 20/20, ~5m17s, input 47566, cache-read 109758, output 7278.

## pi

`pi -p --mode json --provider dashscope --model qwen3.7-plus --no-session`

## qwencode

`qwen --yolo --output-format json --auth-type openai --openai-base-url <url> --model qwen3.7-plus`

## hermes-agent

Live 01–20; usage is the sum of every `API call #N: in=… out=… cache=…/…` line.

## openclaw

`openclaw --profile janetbench-<task> agent --local --json --model bench/qwen3.7-plus --session-id <fresh> --message <prompt>`

Runs under `/usr/bin/node` (24.x; the bundled 22.22.2 is below its minimum) with a
profile and workspace per task, both removed after the run. Usage is
`meta.agentMeta.usage` of the `--json` object only; the reply is
`meta.finalAssistantVisibleText`.

## grok

Temp `GROK_HOME` `[model."qwen3.7-plus"]` with `env_key = "OPENAI_API_KEY"`.

---
Data caveat (2026-09-09): in these recorded reports the qwencode and hermes-agent input-token columns include the cached portion (their CLIs report prompt totals), while pi and janetcode report uncached input. Runs made after this date subtract cache reads for every harness.
