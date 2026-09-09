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

## Replicated on a second model

The same arms on `inception/mercury-2.5`, nine tasks x 8 runs each — 144
runs:

| | passed | context per solved |
|---|---:|---:|
| four tools | 72/72 | **23k** |
| nine tools | 72/72 | 28k |

**x1.23 [1.07, 1.39]** — the same point estimate as qwen3.7-plus, on a
model of a completely different kind, with overlapping intervals. Two
models, 234 runs, one answer.

## And it does not hurt on a large corpus

The obvious objection to the above is that fan-out tasks over small trees
are exactly where a shell one-liner substitutes for a search tool, and that
a large unfamiliar repository is the case the search tools exist for. So
the same arms were run against `evals/large`, five tasks x 4 runs:

| | passed | context per solved |
|---|---:|---:|
| four tools | 20/20 | 54k |
| nine tools | 20/20 | 57k |

**x1.07 [0.83, 1.69]** — inside the noise, and the pass rate is identical.
Withholding the search tools neither helped nor hurt on the corpus they
were supposed to be needed for.

Better on fan-out, neutral on large trees, unchanged pass rate in all 254
runs. `lean_tools` is therefore **on by default**, and the tools are
deferred rather than removed: the worst case is one `tool_search` call.

## The exploration case, finally tested

Both earlier arms ran on generated trees where a shell one-liner
substitutes for a search tool, and both reports named the same missing
case: a genuinely exploratory job on an unfamiliar repository.
`evals/explore` is that — 59 files across eight packages, one constant
defined in one place and hard-coded in exactly one other, findable only by
searching and following the reference.

Five runs per arm on `inception/mercury-2.5`: **5/5 passed both ways**, and
the four-tool arm was cheaper — cost x1.14 and context x1.13 in favour of
the lean surface.

No interval is quoted, because there is one task and a bootstrap over one
task resamples the same value and collapses to a point. The harness used to
print that point as `[1.13, 1.13] · real increase`, which is exactly the
overclaim it exists to prevent; it now says "too few tasks to bootstrap an
interval" and claims nothing. So this is a direction, not a result: on the
one exploratory task that exists, withholding the search tools did not hurt
and looks cheaper.

## What this does not show

That the five tools are worthless — a model that wants one asks for it, and
`tool_search` hands it over with its schema. Nor is the large-corpus arm as
strong as the fan-out one: five tasks x 4 runs on one model, and those
tasks are still tractable with a shell. What is missing from the evidence
is a genuinely exploratory job on an unfamiliar tree, which no eval here
provides.

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
