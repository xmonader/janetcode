# Harness benchmark report

- Model: `inception/mercury-2.5`
- Endpoint: `https://openrouter.ai/api/v1`
- Tasks: 01–20 in four batches of five (01–05, 06–10, 11–15, 16–20)
- When: 2026-09-11T17:09:30Z
- Note: pass/fail is the eval JSON shell check; token fields are only those the harness emitted. Limits: 5m0s wall budget for every harness and a fresh 2m for each check; 40 model calls per task for qwencode, hermes-agent, grok and janetcode (pi has no cap flag); hermes-agent runs with its terminal toolset only; the tasks' max_tool_calls are not enforced for any harness. Input tokens exclude cache reads for every harness (qwencode and hermes-agent report prompt totals that include them; the bench subtracts)

Token columns are **input tokens**, **cache read tokens**, **cache write tokens**, and **output tokens**. Wall time is the measured task duration. A field the harness did not report is `n/a`, not an estimate.

## Per-harness totals

| harness | passed | unavailable | wall time | input tokens | cache read tokens | cache write tokens | output tokens | tool calls | turns | cost USD |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 2/3 | 0 | 2m56.88s | 1230732 | 1253909 | 0 | 16304 | n/a | 97 | 0.056691 |
| janetcode | 1/3 | 0 | 2m31.61s | 1167251 | 390921 | 0 | 19762 | 122 | 100 | 0.051218 |
| qwencode | 3/3 | 0 | 5m7.05s | 13227948 | 7659303 | n/a | 111313 | 579 | 101 | n/a |
| hermes-agent | 1/3 | 0 | 4m8.09s | 724287 | 178523 | n/a | 31746 | n/a | 144 | n/a |
| openclaw | 3/3 | 0 | 4m57.8s | 1449072 | 1641553 | n/a | 21208 | 104 | 78 | n/a |

## Per-task wall time

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 1m12.64s | 1m17.04s | 2m28.5s | 1m42.29s | 1m58.81s |
| 42-session-records | 46.69s | 24.99s | 1m15.33s | 1m9.48s | 1m32.58s |
| 43-session-python-ledger | 57.54s | 49.58s | 1m23.22s | 1m16.32s | 1m26.4s |

## Per-task input tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 557576 | 607480 | 7106300 | 298431 | 635771 |
| 42-session-records | 395683 | 132404 | 3188769 | 272408 | 356879 |
| 43-session-python-ledger | 277473 | 427367 | 2932879 | 153448 | 456422 |

## Per-task cache read tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 615043 | 192517 | 5330945 | 69176 | 776889 |
| 42-session-records | 314597 | 56152 | 1081979 | 53149 | 460634 |
| 43-session-python-ledger | 324269 | 142252 | 1246379 | 56198 | 404030 |

## Per-task cache write tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 0 | 0 | n/a | n/a | n/a |
| 42-session-records | 0 | 0 | n/a | n/a | n/a |
| 43-session-python-ledger | 0 | 0 | n/a | n/a | n/a |

## Per-task output tokens

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | 7636 | 8600 | 58068 | 11519 | 9168 |
| 42-session-records | 2611 | 2719 | 12704 | 9748 | 5240 |
| 43-session-python-ledger | 6057 | 8443 | 40541 | 10479 | 6800 |

## Per-task pass/fail

| task | pi | janetcode | qwencode | hermes-agent | openclaw |
|---|---:|---:|---:|---:|---:|
| 41-session-go-service | PASS | PASS | PASS | PASS | PASS |
| 42-session-records | FAIL | FAIL | PASS | FAIL | PASS |
| 43-session-python-ledger | PASS | FAIL | PASS | FAIL | PASS |

## Session context growth

Per prompt of each session task: input + cache tokens the harness read during that prompt (the last column is the task total). A flat row means the history was held down; a rising row means it was resent whole.

### 41-session-go-service

| harness | p1 | p2 | p3 | p4 | p5 | p6 | p7 | total |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| pi | 76148 | 344443 | 107559 | 202456 | 248416 | 129814 | 63783 | 1172619 |
| janetcode | 13879 | 104243 | 54608 | 117281 | 259212 | 163724 | 87050 | 799997 |
| qwencode | 86163 | 923099 | 1128397 | 1785868 | 2614093 | 2882192 | 3017433 | 12437245 |
| hermes-agent | 23690 | 103986 | 67472 | 58264 | 50699 | 32546 | 30950 | 367607 |
| openclaw | 141742 | 352161 | 210659 | 175333 | 285327 | 147514 | 99924 | 1412660 |

### 42-session-records

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 129160 | 93995 | 96542 | 97724 | 183686 | 109173 | 710280 |
| janetcode | 37930 | 22398 | 25309 | 25667 | 46310 | 30942 | 188556 |
| qwencode | 230865 | 475983 | 627042 | 728412 | 1048148 | 1160298 | 4270748 |
| hermes-agent | 25668 | 28490 | 63890 | 82714 | 76013 | 48782 | 325557 |
| openclaw | 171061 | 105846 | 105494 | 106380 | 217595 | 111137 | 817513 |

### 43-session-python-ledger

| harness | p1 | p2 | p3 | p4 | p5 | p6 | total |
|---|---:|---:|---:|---:|---:|---:|---:|
| pi | 54038 | 82561 | 44881 | 279613 | 83628 | 57021 | 601742 |
| janetcode | 16341 | 97474 | 123037 | 120492 | 163267 | 49008 | 569619 |
| qwencode | 85898 | 322913 | 480127 | 764675 | 1198733 | 1326912 | 4179258 |
| hermes-agent | 19588 | 32568 | 52046 | 46041 | 37516 | 21887 | 209646 |
| openclaw | 105627 | 151970 | 79372 | 211216 | 221761 | 90506 | 860452 |

## Failures and unavailable harnesses

- **pi / 42-session-records** (batch 1): check failed: exit status 1: check: inventory.txt does not reflect the renames (prompt 6)
  - command: `pi -p --mode json --provider openrouter --model inception/mercury-2.5 --session-id 07a2469f-fa7f-4fad-8864-7b8d967e37c7 --session-dir /tmp/pi-sessions-1803555424 Update inventory.txt so it reflects the renames you made in the previous step (the archived ids appear with their new prefix; keep it sorted, one id per line). Then reply with just the number of files you changed in the previous step.`
  - stderr: check: inventory.txt does not reflect the renames (prompt 6)
- **janetcode / 42-session-records** (batch 1): check failed: exit status 1: check: inventory.txt does not reflect the renames (prompt 6)
  - command: `janetcode --eval --eval-only 42-session-records --provider openai --model inception/mercury-2.5`
  - stderr: running 3 eval tasks × 1 with openai/inception/mercury-2.5 (3 at a time)
- **janetcode / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `janetcode --eval --eval-only 43-session-python-ledger --provider openai --model inception/mercury-2.5`
  - stderr: running 3 eval tasks × 1 with openai/inception/mercury-2.5 (3 at a time)
- **hermes-agent / 42-session-records** (batch 1): check failed: exit status 1: check: inventory.txt does not reflect the renames (prompt 6)
  - command: `hermes-agent --query Update inventory.txt so it reflects the renames you made in the previous step (the archived ids appear with their new prefix; keep it sorted, one id per line). Then reply with just the number of files you changed in the previous step. --model inception/mercury-2.5 --base_url https://openrouter.ai/api/v1 --api_key *** --max_turns 40 --enabled_toolsets terminal --verbose`
  - stderr: ptTokensDetails(audio_tokens=0, cached_tokens=770, cache_write_tokens=0, video_tokens=0), cost=0.00048277, is_byok=False, cost_details={'upstream_inference_cost': 0.00048277, 'upstream_inference_prompt_cost': 0.00046732, 'upstream_inference_completions_cost': 1.545e-05})
- **hermes-agent / 43-session-python-ledger** (batch 1): check failed: exit status 1
  - command: `hermes-agent --query Run the tests and fix anything that fails. When they pass, reply with only the name of the CLI option you added and nothing else. --model inception/mercury-2.5 --base_url https://openrouter.ai/api/v1 --api_key *** --max_turns 40 --enabled_toolsets terminal --verbose`
  - stderr: TokensDetails(audio_tokens=0, cached_tokens=4981, cache_write_tokens=0, video_tokens=0), cost=0.000242254, is_byok=False, cost_details={'upstream_inference_cost': 0.000242254, 'upstream_inference_prompt_cost': 0.000114604, 'upstream_inference_completions_cost': 0.00012765})

