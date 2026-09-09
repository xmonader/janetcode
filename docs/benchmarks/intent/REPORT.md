# What does asking for a reason on every tool call cost?

Every tool call carries a required one-line `intent` — the model's own
statement of why it is making the call, in the user's terms. The idea comes
from `jcode`, where a 3h10m session of 123 tool calls compresses to a 2.2 KB
narrative made of nothing but those lines.

It is not free: it is output tokens, and output tokens are what per-turn
latency tracks.

## Measured

Nine fanout tasks x 3 runs per arm, `qwen3.7-plus`, `--eval-off intent`
against the default.

| | passed | context per solved | output |
|---|---:|---:|---:|
| without intent | 27/27 | 19k | 25k |
| with intent | 27/27 | 21k | 26k |

Paired bootstrap over the nine shared tasks:

- context **x1.10 [0.92, 1.31]** — inside the noise
- output **x1.07 [0.83, 1.38]** — inside the noise
- pass rate unchanged

So it costs something, both point estimates say so, and neither clears the
run-to-run spread. On a 25k output budget a 7% point estimate is about
1.7k tokens across 27 runs — under two seconds a run at the generation
rates measured on sonnet.

## Kept on, and why that is a judgement rather than a result

What it buys is not measured here at all: a label the user reads while a
call runs, a record of intent that survives compaction, and a session
narrative for resuming or forking. Those are real and none of them are in
the table above.

The honest summary is that the cost is small and not demonstrated, the
benefit is real and not quantified, and the default is a product decision
rather than a finding. `intent: false` in the config turns it off, and
`--eval-off intent` turns it off for a measurement.

Two implementation details decide whether it works at all, both learned the
hard way:

- **It must be required.** As an optional property the model omitted it on
  every single call. Made required, it produced one on every call.
- **It must be stripped before dispatch.** The tools decode with unknown
  fields disallowed, so an intent left on the input fails every call that
  carries one.

## Reproduce

```
janetcode --eval evals/fanout --eval-runs 3 --eval-off intent,script --eval-json a.json
janetcode --eval evals/fanout --eval-runs 3 --eval-off script --eval-json b.json --eval-compare a.json
```
