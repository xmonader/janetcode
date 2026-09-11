# Harness benchmark report

- Model: `inception/mercury-2.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-11T17:00:47Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 3/3 | 0 | 2m7.75s | 959459 | 867859 | 0 | 14597 | n/a | 75 | 0.044039 |
| janetcode | 2/3 | 0 | 2m15.82s | 1011244 | 333613 | 0 | 18598 | 124 | 88 | 0.044574 |
| qwencode | 3/3 | 0 | 5m24.78s | 11809555 | 7347465 | n/a | 93137 | 545 | 104 | n/a |
| hermes-agent | 1/3 | 0 | 3m51.7s | 808855 | 167795 | n/a | 28494 | n/a | 152 | n/a |
| openclaw | 2/3 | 0 | 6m8.97s | 1873320 | 2208075 | n/a | 29955 | 120 | 97 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 50.94s | 54.77s | 2m47.99s | 1m29.99s | 2m41.93s |
| 42-session-records | 34.8s | 24.22s | 1m8.28s | 1m4.99s | 1m30.93s |
| 43-session-python-ledger | 42s | 56.82s | 1m28.51s | 1m16.71s | 1m56.1s |

## Per-task input tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 367841 | 362817 | 4814411 | 256355 | 854138 |
| 42-session-records | 301864 | 184794 | 3434677 | 311456 | 460161 |
| 43-session-python-ledger | 289754 | 463633 | 3560467 | 241044 | 559021 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 369539 | 127863 | 4808229 | 70470 | 1180866 |
| 42-session-records | 311254 | 69741 | 1347606 | 32696 | 463178 |
| 43-session-python-ledger | 187066 | 136009 | 1191630 | 64629 | 564031 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 0 | 0 | n/a | n/a | n/a |
| 42-session-records | 0 | 0 | n/a | n/a | n/a |
| 43-session-python-ledger | 0 | 0 | n/a | n/a | n/a |

## Per-task output tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 6381 | 7450 | 42686 | 10114 | 13473 |
| 42-session-records | 2174 | 2655 | 12299 | 6910 | 5851 |
| 43-session-python-ledger | 6042 | 8493 | 38152 | 11470 | 10631 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | PASS | PASS | PASS | PASS | PASS |
| 42-session-records | PASS | PASS | PASS | FAIL | PASS |
| 43-session-python-ledger | PASS | FAIL | PASS | FAIL | FAIL |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 94224 | 166074 | 76375 | 107302 | 144127 | 89055 | 60223 | 737380 |
| janetcode | 14088 | 69992 | 39804 | 143228 | 115283 | 53613 | 54672 | 490680 |
| qwencode | 84575 | 311735 | 506264 | 1259055 | 1615357 | 2856534 | 2989120 | 9622640 |
| hermes-agent | 14468 | 47827 | 47299 | 97429 | 29729 | 27536 | 62537 | 326825 |
| openclaw | 180548 | 205714 | 215224 | 413406 | 757068 | 156083 | 106961 | 2035004 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 152854 | 71092 | 70961 | 71110 | 172252 | 74849 | 613118 |
| janetcode | 43625 | 32030 | 44560 | 34885 | 60498 | 38937 | 254535 |
| qwencode | 284748 | 430148 | 728470 | 933916 | 1146687 | 1258314 | 4782283 |
| hermes-agent | 25603 | 95729 | 48541 | 47889 | 98797 | 27593 | 344152 |
| openclaw | 172589 | 176666 | 105480 | 177363 | 181137 | 110104 | 923339 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 55999 | 85656 | 45401 | 151861 | 82211 | 55692 | 476820 |
| janetcode | 16377 | 52679 | 64862 | 200642 | 212209 | 52873 | 599642 |
| qwencode | 129348 | 358994 | 554536 | 895565 | 1339172 | 1474482 | 4752097 |
| hermes-agent | 13248 | 53190 | 55488 | 22962 | 71375 | 89410 | 305673 |
| openclaw | 216323 | 116229 | 120048 | 255596 | 320594 | 94262 | 1123052 |

## Failures and unavailable harnesses

- **janetcode / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `janetcode --eval --eval-only 43-session-python-ledger --provider openai --model inception/mercury-2.5`
  - stderr: running 3 eval tasks × 1 with openai/inception/mercury-2.5 (3 at a time)
- **hermes-agent / 42-session-records** (batch 1): check failed: exit status 1: check: inventory.txt does not reflect the renames (prompt 6)
  - command: `hermes-agent --query Update inventory.txt so it reflects the renames you made in the previous step (the archived ids appear with their new prefix; keep it sorted, one id per line). Then reply with just the number of files you changed in the previous step. --model inception/mercury-2.5 --base_url https://openrouter.ai/api/v1 --api_key *** --max_turns 40 --enabled_toolsets terminal --verbose`
  - stderr: PromptTokensDetails(audio_tokens=0, cached_tokens=765, cache_write_tokens=0, video_tokens=0), cost=0.00038325, is_byok=False, cost_details={'upstream_inference_cost': 0.00038325, 'upstream_inference_prompt_cost': 0.0003735, 'upstream_inference_completions_cost': 9.75e-06})
- **hermes-agent / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else. --model inception/mercury-2.5 --base_url https://openrouter.ai/api/v1 --api_key *** --max_turns 40 --enabled_toolsets terminal --verbose`
  - stderr: ails(audio_tokens=0, cached_tokens=1822, cache_write_tokens=0, video_tokens=0), cost=0.000433778, is_byok=False, cost_details={'upstream_inference_cost': 0.000433778, 'upstream_inference_prompt_cost': 0.000340928, 'upstream_inference_completions_cost': 9.285e-05})
- **openclaw / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `openclaw --profile janetbench-harness-openclaw-43-session-python-ledger-1747743290 agent --local --json --model bench/inception/mercury-2.5 --session-id bench-27a05002-3879-48f7-a97b-931f1dd1587a --message Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else.`
  - stderr: openai embeddings failed: 401 { "error": { "message": "Incorrect API key provided: sk-or-v1*************************************************************b17b. You can find your API key at https://platform.openai.com/account/api-keys.", "type": "invalid_request_error", "code": "invalid_api_key", "param": null }, "status": 401 }[39m

