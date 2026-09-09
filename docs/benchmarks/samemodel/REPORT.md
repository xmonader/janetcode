# Same model, same endpoint, two harnesses

Every earlier janetcode/Claude Code comparison in this repository compared
two *endpoints* as much as two harnesses: Claude Code ran on an Anthropic
account, janetcode on OpenRouter. The headline finding from those runs —
janetcode is 1.75x slower on sonnet — could therefore have been about the
route, the model, or the harness, and there was no way to tell which.

This run removes the confound. Both harnesses run `inception/mercury-2.5`
through the same OpenRouter endpoint, on the same three multi-prompt
session tasks.

## How Claude Code was pointed at a third-party model

Claude Code validates the model name against its own catalog before it
makes any request, and refuses `inception/mercury-2.5` outright — no HTTP
call is attempted. Pointing `ANTHROPIC_BASE_URL` at OpenRouter's
Anthropic-compatible `/v1/messages` endpoint is not enough on its own.

What works is a small proxy that accepts a model name Claude Code does
know, rewrites it, and forwards:

```
ANTHROPIC_BASE_URL=http://127.0.0.1:8820
ANTHROPIC_AUTH_TOKEN=<anything>
CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
```

The proxy (about sixty lines) swaps the `model` field for the one under
test and strips the duplicate `/v1` prefix, since Claude Code posts to
`/v1/messages?beta=true` while the OpenRouter root already ends in `/v1`.
Verified before use: a request naming sonnet comes back marked
`inception/mercury-2.5`.

## The result

| | passed | input tokens | cache read | output | turns | wall |
|---|---:|---:|---:|---:|---:|---:|
| Claude Code | 2/3 | 2,198,886 | 875,303 | 18,565 | 136 | 166s |
| **janetcode** | **3/3** | **1,177,097** | 416,325 | 23,680 | **97** | **141s** |

Per task:

| task | Claude Code | janetcode |
|---|---|---|
| 41-session-go-service | PASS · 72s · 55 turns · 825k in | PASS · 64s · 43 turns · 560k in |
| 42-session-records | PASS · 36s · 24 turns · 496k in | PASS · 35s · 31 turns · 325k in |
| 43-session-python-ledger | **FAIL** · 58s · 57 turns · 878k in | PASS · 42s · 23 turns · 291k in |

janetcode used **47% fewer input tokens**, took fewer turns, finished
sooner, and passed a task Claude Code failed.

## What this says about the sonnet result

The 1.75x slowness measured on sonnet does not reproduce when the model is
held constant. On this model janetcode is the faster harness, so that gap
was a property of the model and route rather than of the harness — which is
what the earlier report suspected but could not show.

Note that Claude Code is still *quicker per turn* here: 1.22s against 1.46s.
It takes more of them. The pattern from the sonnet runs — the harness that
writes less per turn wins the wall clock — holds in both directions, and on
this model janetcode is the one taking fewer steps.

## Caveats

One run per cell, and the sets are not perfectly paired: Claude Code failed
`43-session-python-ledger`, and a failed run's cost is not comparable to a
passing one's. Excluding that task entirely, the input-token totals are
1,321,174 against 885,614 — still a 33% difference in janetcode's favour,
so the conclusion does not rest on the failure.

One model. Mercury is a diffusion model with unusual latency
characteristics, and nothing here transfers to sonnet unexamined — that
measurement still needs an account with credit on a provider serving it.

## Reproduce

```
python3 px.py 8820 &          # the rewriting proxy, see this directory
ANTHROPIC_BASE_URL=http://127.0.0.1:8820 ANTHROPIC_AUTH_TOKEN=proxy \
  ./bin/harnessbench --evals evals/session --filter session \
  --harnesses claudecode --claude-model sonnet --out cc-merc
./bin/harnessbench --evals evals/session --filter session --harnesses janetcode \
  --model inception/mercury-2.5 --base-url https://openrouter.ai/api/v1 --out jc-merc
```
