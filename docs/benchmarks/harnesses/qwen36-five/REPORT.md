# Five harnesses on qwen3.6-plus, three times each

- Model: `qwen3.6-plus` at `https://coding-intl.dashscope.aliyuncs.com/v1`
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md)

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **58/60** | **286,219** | 662,376 | 18,579 | **804s** | **1%** |
| pi | **58/60** | 4,093,574 | 0 | 23,055 | 1106s | 13% |
| qwencode | **58/60** | 2,626,378 | 5,634,794 | 24,075 | 1163s | 3% |
| hermes-agent | 57/60 | 1,384,325 | not reported | 33,777 | 1597s | 8% |
| openclaw | 57/60 | 7,796,214 | not reported | 26,827 | 1615s | 10% |

Fresh input and cache read are kept apart because a cache read costs about
a tenth as much. On this model the endpoint's cache accounting is uneven:
janetcode and qwencode received cache-read counts on every call, pi
received `cacheRead: 0` on every one of its 60 tasks, and hermes-agent and
openclaw got no figure at all. Where a harness reports no cache read, its
fresh-input column is the whole prompt; the pi, hermes-agent and openclaw
figures above are therefore upper bounds on what they paid fresh, and are
not directly comparable with the two that were credited.

On what was reported, janetcode is lowest, hermes-agent 4.8x, qwencode
9.2x, pi 14.3x and openclaw 27.2x. janetcode's fresh input is within 1% of
its qwen3.7-plus figure (286k against 289k), which says the harness's
prefix strategy holds across the two model versions; qwencode's is the
same story (2.63M against 2.60M).

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 95,760 · 95,270 · 95,189 |
| pi | 1,393,021 · 1,270,143 · 1,430,410 |
| qwencode | 865,842 · 867,026 · 893,510 |
| hermes-agent | 483,045 · 455,727 · 445,553 |
| openclaw | 2,504,242 · 2,752,462 · 2,539,510 |

Every harness reproduces to within 13% on this model, against pi's 114%
on qwen3.7-plus and 294% on mercury. janetcode's three runs are within
0.6% of each other.

## What each harness got wrong

Twelve failures in 300 runs, on four tasks.

**`06-read-and-answer`** — "What does Compute(3) return? Reply with just
the number" — cost openclaw all three runs, and pi and qwencode one each.
The three openclaw replies all begin "I don't have a definition of
Compute": it never opened the file that was in its workspace. pi and
qwencode read it and answered `3`, where the loop returns 15. This is the
one task judged from the final message, and with this model it is the
weakest: 5 of 15 attempts failed, all on the model's side.

**`17-csv-sort`** fell to janetcode twice, and to qwencode and hermes-agent
once each. In the qwen3.7-plus series the cause found for janetcode was
the model writing the file through its Python sandbox with CRLF line
endings, against a byte-for-byte check. The task directories are not kept
here, so that is the likely cause rather than a verified one; the choice
is stochastic and left unsteered for the reasons given in that report.

**`18-find-mentions`** — list the files that mention a phrase — timed out
at three minutes once for hermes-agent and once for pi. Both harnesses
were still running when the budget ran out.

**`14-gitignore`** failed once for hermes-agent.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen3.6-plus --base-url https://coding-intl.dashscope.aliyuncs.com/v1 --out run1
```

pi reaches DashScope through a `dashscope` provider entry in its
`models.json`, which must list the model id.
