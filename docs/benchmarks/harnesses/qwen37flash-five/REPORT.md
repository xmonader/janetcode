# Five harnesses on qwen3.7-flash, three times each

- Model: `qwen/qwen3.7-flash` at `https://openrouter.ai/api/v1`
  ($0.03 per million input, $0.006 cache read, $0.13 output at list)
- Tasks: `evals/` 01–20, three complete repeats, identical endpoint and
  identical seeded task directories for every harness
- 300 task runs in total, recorded 2026-09-10 with the same launcher as
  the [qwen3.7-plus series](../qwen-five/REPORT.md). Two other models'
  series ran on the same endpoint at the same time, so wall time here is
  under shared load.

## Totals over three runs

| harness | passed | fresh input | cache read | output | wall | run-to-run spread |
|---|---:|---:|---:|---:|---:|---:|
| **janetcode** | **60/60** | 298,758 | **811,228** | **27,707** | **560s** | **4%** |
| pi | 58/60 | **241,894** | 4,308,736 | 27,832 | 748s | 389% |
| qwencode | **60/60** | 3,566,825 | 5,710,080 | 27,956 | 747s | 25% |
| hermes-agent | 59/60 | 944,613 | 383,360 | 37,286 | 995s | 11% |
| openclaw | 55/60 | 2,643,749 | 6,005,632 | 35,389 | 1131s | 56% |

The flash sibling of the model this series began with, at a tenth of its
price. janetcode's fresh input is within 4% of its qwen3.7-plus figure
(299k against 289k) and it passes everything again, as does qwencode. At
list price the 60 tasks cost janetcode 1.7¢, hermes-agent 3.6¢, pi 3.7¢,
openclaw 12¢ and qwencode 14¢.

hermes-agent got no cache figure on 20 of its 60 tasks, so its fresh
column is partly uncredited prefix.

## Reproducibility

| harness | fresh input per run |
|---|---|
| janetcode | 101,536 · 99,405 · 97,817 |
| pi | 170,890 · 34,921 · 36,083 |
| qwencode | 1,147,084 · 1,346,231 · 1,073,510 |
| hermes-agent | 302,218 · 305,810 · 336,585 |
| openclaw | 1,076,338 · 875,685 · 691,726 |

pi's 389% is the endpoint's cache credit, not pi: its prompt totals
(fresh plus cached) per run were 1,619,082 · 1,412,073 · 1,519,475, within
15%, while the share charged fresh went from 11% in the first run to 2%
in the other two. janetcode's prompt totals were 413,614 · 356,843 ·
339,529, and its fresh figures stayed within 4% regardless.

## What each harness got wrong

Eight failures in 300 runs.

**openclaw** lost five: `06-read-and-answer` twice — once "I don't
recognize Compute", once a JSON object in place of the number — and
`13-multi-file-package` twice with the nested `stack/go.mod` seen on
other models, plus `05-rename-symbol` once.

**pi** lost `06-read-and-answer` once, answering that it lacked context
without opening the file, and `16-readme` once.

**hermes-agent** lost `17-csv-sort` once.

**janetcode** and **qwencode** passed 60/60.

## Reproduce

```
./bin/harnessbench --evals evals \
  --harnesses janetcode,pi,hermes-agent,qwencode,openclaw \
  --model qwen/qwen3.7-flash --base-url https://openrouter.ai/api/v1 --out run1
```
