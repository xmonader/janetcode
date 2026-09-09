# What does a wide tool surface cost?

Three other harnesses independently converged on a very small tool surface:
`pi` ships four tools by default (`read`, `bash`, `edit`, `write`) and
routes search through the shell; `jcode`'s tool descriptions average 46
characters and its system prompt is 34 lines with no tool-usage prose at
all; `mistral-vibe`'s seventeen tool descriptions total 13.8 KB. All three
argue the same thing — instruction text that rides on every request is paid
forever — and all three enforce behaviour with refusals and errors instead
of prose.

janetcode sends 22 tool schemas, about 4.5 KB, in the prefix of every
request. So the question is measurable rather than aesthetic: what does
that actually cost?

## The experiment

`--eval-off extratools` strips the eval agent from nine tools to the same
four `pi` ships. Nine fanout tasks x 3 runs per arm, `qwen3.7-plus`,
`execute_code` disabled on both sides so the comparison is only about the
surface.

| | passed | context per solved | output | wall |
|---|---:|---:|---:|---:|
| four tools | 27/27 | **15k** | 22k | 11m15s |
| nine tools | 27/27 | 19k | 25k | 11m29s |

Paired bootstrap over the nine shared tasks:

- context **x1.23 [1.10, 1.44]** — a real increase, the interval clears 1
- output x1.15 [0.84, 1.63] — inside the noise
- pass rate unchanged, 27/27 either way

**Five extra tools cost 23% of context for no measurable benefit on this
task set.** That is far more than the schemas themselves weigh: 4.5 KB of
schema is roughly 1.1k tokens against a 15k-per-task baseline. The rest is
behavioural — with `list_dir`, `glob`, `grep` and `repo_map` available the
model takes more, smaller steps; with only a shell it composes one pipeline.

## What this does not show

That the five tools are worthless. These are fan-out tasks over small
trees, which is exactly where a shell one-liner substitutes for a search
tool. A large unfamiliar repository is the case `repo_map` and `grep` exist
for, and it is not in this set. Nor does it transfer to another model
unexamined: the whole result is one model on nine tasks.

It also did not make anything faster — 11m15s against 11m29s, well inside
the noise — so this is a context and cost result, not the latency win the
wall-clock gap needs.

What it does establish is that the surface is not free, that the cost is
larger than the schema bytes suggest, and that the three harnesses arguing
for small surfaces have a measurement behind them here.

## Reproduce

```
janetcode --eval evals/fanout --eval-runs 3 --eval-off extratools,script --eval-json a.json
janetcode --eval evals/fanout --eval-runs 3 --eval-off script --eval-json b.json --eval-compare a.json
janetcode --eval-report b.json --eval-compare a.json   # offline, no API calls
```
