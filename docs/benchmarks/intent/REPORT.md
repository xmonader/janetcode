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

## With more runs, and measured together with a second schema field, it is not

The 27-run arms above could not resolve a ten percent effect. Six runs per
task on nine tasks — 108 runs — measured against a baseline with both
`intent` and the `accept_large_output` opt-in removed:

| | ratio | |
|---|---|---|
| context per solved | **x1.21 [1.15, 1.26]** | real increase |
| output tokens | **x1.25 [1.17, 1.34]** | real increase |
| cost per solved | x1.10 [1.05, 1.15] | real increase |

54/54 passed either way. Twenty-one percent more context and twenty-five
percent more output, for a benefit nothing has measured.

This also explained a regression that had appeared elsewhere: the sonnet
session benchmark came back 24% worse in context than the same tasks
earlier the same day, and these two fields were the difference.

**So both defaults changed.** `intent` is off unless asked for, and the
`accept_large_output` opt-in was taken off every tool schema entirely — the
refusal that needs it names it instead, which is the only place it is
relevant. A property declared on every tool is paid for on every request
forever; a sentence in a refusal is paid for when it is needed.

Re-measured after the change, against the same baseline: context x1.01
[0.92, 1.12], output x1.04 [0.92, 1.16], both inside the noise. The
regression is gone.

## Why it was kept on, and why that judgement was wrong

What it buys is not measured here at all: a label the user reads while a
call runs, a record of intent that survives compaction, and a session
narrative for resuming or forking. Those are real and none of them are in
the table above.

That reasoning held while the cost looked small and undemonstrated. It was
neither: it was 21% of context, and the earlier arms were simply too few
runs to see it. The benefit is still real and still unquantified, which is
exactly why it should not be spent by default. `intent: true` turns it on.

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
