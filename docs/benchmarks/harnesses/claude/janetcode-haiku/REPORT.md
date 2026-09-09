# Harness benchmark report

- Model: `anthropic/claude-haiku-4.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-09T12:31:54Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 6m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | 0/3 | 0 | 222ms | 0 | 0 | 0 | 0 | 0 | 0 | n/a |

## Per-task wall time

| task | janetcode |
|---|---:|
| 41-session-go-service | 74ms |
| 42-session-records | 77ms |
| 43-session-python-ledger | 71ms |

## Per-task input tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task cache read tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task cache write tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task output tokens

| task | janetcode |
|---|---:|
| 41-session-go-service | 0 |
| 42-session-records | 0 |
| 43-session-python-ledger | 0 |

## Per-task pass/fail

| task | janetcode |
|---|---:|
| 41-session-go-service | FAIL |
| 42-session-records | FAIL |
| 43-session-python-ledger | FAIL |

## Failures and unavailable harnesses

- **janetcode / 41-session-go-service** (batch 1): run: Missing Authentication header (HTTP 401)
  - command: `janetcode --eval --eval-only 41-session-go-service --provider openai --model anthropic/claude-haiku-4.5`
  - stderr: running 3 eval tasks × 1 with openai/anthropic/claude-haiku-4.5 (3 at a time)
- **janetcode / 42-session-records** (batch 1): run: Missing Authentication header (HTTP 401)
  - command: `janetcode --eval --eval-only 42-session-records --provider openai --model anthropic/claude-haiku-4.5`
  - stderr: running 3 eval tasks × 1 with openai/anthropic/claude-haiku-4.5 (3 at a time)
- **janetcode / 43-session-python-ledger** (batch 1): run: Missing Authentication header (HTTP 401)
  - command: `janetcode --eval --eval-only 43-session-python-ledger --provider openai --model anthropic/claude-haiku-4.5`
  - stderr: running 3 eval tasks × 1 with openai/anthropic/claude-haiku-4.5 (3 at a time)

