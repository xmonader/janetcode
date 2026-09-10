# Five harnesses on the hard set, qwen3.7-plus, three times each

- Model: `qwen3.7-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: `evals/` 21–26, the six tagged `hard` (a data race, an interface
  refactor, an INI parser against hidden tests, a repository-wide rename,
  a set of pagination bugs, a log-summary shell script), 25–40 tool calls
  allowed each, a ten-minute budget per task, the model-call cap lifted
  to 60 so it never binds
- Three complete repeats, identical seeded task directories for every
  harness; 90 task runs in total, recorded 2026-09-10

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **18/18** | **136,567** | **351,642** | 70,003 | 1490s | **6%** |
| pi | **18/18** | 268,946 | 2,026,112 | 62,718 | 1539s | 16% |
| qwencode | 16/18 | 830,185 | 2,717,335 | **35,259** | **942s** | 2% |
| hermes-agent | 17/18 | 235,538 | 640,128 | 63,906 | 1628s | 89% |
| openclaw | **18/18** | 541,501 | 2,818,816 | 67,455 | 1735s | 20% |

At list price ($0.32 per million fresh, $0.064 cache read, $0.128
output) the 18 tasks cost janetcode 16¢, hermes-agent 20¢, pi 30¢,
openclaw 44¢ and qwencode 48¢. On fresh input the ordering is the one the
easy set gave: janetcode lowest, hermes-agent 1.7x, pi 2.0x, openclaw
4.0x, qwencode 6.1x.

## The set is not hard enough to separate the harnesses

87 of 90 passed. Three harnesses passed everything three times. The set
was built to expose the difference between harnesses that the twenty easy
tasks could not; on this model it does not, and the failures it does
produce are all one task.

What it does show is where the harnesses spend. The per-task means over
three runs:

| task | janetcode | pi | hermes-agent | openclaw | qwencode |
|---|---|---|---|---|---|
| 21 data race | 4 calls · 33s | 5 · 39s | 5 · 44s | 5 · 47s | 4 · 32s |
| 22 interface refactor | 5 · 37s | 8 · 61s | 7 · 72s | 5 · 58s | 4 · 44s |
| 23 INI parser | 3 · 254s · 14k out | 4 · 207s · 11k out | 5 · 150s · 7.6k out | 3 · 162s · 8.5k out | 3 · 73s · 3.4k out |
| 24 rename everywhere | 5 · 35s | 7 · 74s | 6 · 57s | 6 · 94s | 6 · 57s |
| 25 pagination bugs | 5 · 55s | 6 · 57s | 13 · 156s | 6 · 113s | 4 · 51s |
| 26 log script | 6 · 83s | 7 · 75s | 6 · 65s | 5 · 105s | 5 · 57s |

## The one task that bites, and why the output column is up

`23-python-ini-parser` is judged by a hidden test file the harness never
sees: quoted values, trailing comments, booleans and integers, sections
with spaces. It failed qwencode twice and hermes-agent once — the only
three failures — and it is also where every harness wrote the most
output by a factor of four to ten.

janetcode wrote 14k output tokens on it per run and took four minutes,
against qwencode's 3.4k and 73 seconds. That is not waste: janetcode's
model wrote its own tests for the rules in the prompt and ran them, and
passed the hidden ones 3/3; qwencode wrote the parser once, said done,
and failed 2/3. On this task the output tokens bought the pass. The
harness does not force this; it is the model's choice under janetcode's
prompt, which asks for evidence before a claim. It is also why
janetcode's output total across the set (70k) is twice qwencode's (35k)
while its input is a sixth.

hermes-agent's 89% spread is one task in one run: `25-pagination-bugs`
in the first run took 13 calls and 49k fresh input (it reached the
answer on its own, and passed); the other two runs took 4–5 calls.

## Reproduce

```
./bin/harnessbench --evals evals --filter all --only hard \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen3.7-plus --base-url https://coding-intl.dashscope.aliyuncs.com/v1 \
  --timeout 10m --max-turns 60 --out run1
```
