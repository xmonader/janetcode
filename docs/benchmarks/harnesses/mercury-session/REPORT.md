# Five harnesses on the session set, mercury-2.5, three times each

- Model: `inception/mercury-2.5` at `https://openrouter.ai/api/v1`
  ($0.04 per million fresh, $0.15 output at list)
- Tasks: `evals/session/` 41–43, the multi-prompt session set (a Go
  service over seven prompts, a records tree over six, a Python ledger
  over six), five minutes per task, identical seeded directories for
  every harness; one continuing session per task for every harness that
  can continue one (hermes-agent cannot and starts fresh per prompt)
- Three complete repeats, 45 task runs in total, recorded 2026-09-11.
  This series stands in for the third qwen3.7-plus run that DashScope's
  quota lost (`../qwen-session/`)

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| janetcode | 5/9 | 3,231,029 | 1,017,403 | 58,136 | 426s | 15% |
| pi | **8/9** | 3,384,264 | 2,675,853 | 48,995 | 442s | 28% |
| qwencode | **8/9** | 36,768,078 | 19,813,642 | 284,492 | 897s | 13% |
| hermes-agent | 4/9 | **2,303,180** | 513,118 | 90,355 | 721s | 12% |
| openclaw | 7/9 | 5,355,909 | 5,394,808 | 74,320 | 985s | 40% |

Fresh input per run: janetcode 1.05M · 1.01M · 1.17M, pi 1.19M · 0.96M ·
1.23M, qwencode 11.7M · 11.8M · 13.2M, hermes-agent 0.77M · 0.81M ·
0.72M, openclaw 2.0M · 1.9M · 1.4M.

## janetcode loses on this model, and the loss is one task

This is the first series in which janetcode is not first or joint first
on pass rate. On qwen3.7-plus every continuing harness went 6/6 on this
set; on mercury-2.5, a much weaker and much cheaper model, janetcode is
5/9 against pi's and qwencode's 8/9.

Every janetcode failure but one is `43-session-python-ledger`, which it
failed in all three runs; the other is `42-session-records` in run 3.
The same two tasks are where every other harness failed too (43 took
one run each from qwencode and openclaw and three from hermes-agent; 42
took one from pi), so the tasks are the hard part of the set on this
model; janetcode is simply the harness the model failed them under most.

The one failure caught with its directory kept (task 42, reproduced
under the launcher's conditions: empty private home, three tasks at a
time) is a model miss, not a harness one: prompt 6 asks for the archived
ids to appear in inventory.txt with their new prefix "keep it sorted",
and the file had the renames in place and the original order.

Task 43 did not reproduce outside the launcher: seven runs of the task
alone through the eval driver on the same endpoint passed 7/7, at 33 to
61 tool calls, and two runs of the whole set under the launcher's
environment passed it 2/2. What differs between those and the three
launcher runs that failed it is not established; it is the open item of
this series.

## Context

janetcode read the least of the continuing harnesses that pass (pi reads
the same fresh input and 2.6x the cache; qwencode 11x; openclaw 1.7x
fresh and 5x cache). hermes-agent reads less fresh input because each
prompt starts a new conversation, and it passes 4/9 for the same reason.

## Reproduce

```
./bin/harnessbench --evals evals/session --filter session \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model inception/mercury-2.5 --base-url https://openrouter.ai/api/v1 \
  --timeout 5m --out run1
```
