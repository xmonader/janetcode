# janetcode against Claude Code, on Claude models

- Tasks: the three multi-prompt sessions in `evals/session` (six and seven
  dependent prompts each)
- janetcode: `anthropic/claude-haiku-4.5` and `anthropic/claude-sonnet-4.5`
  through OpenRouter
- Claude Code: `--model haiku` and `--model sonnet` on its own account
- When: 2026-09-09T12:47:17Z
- Cost is what each harness reported, not an estimate

This compares two harnesses on comparable models. It is not a comparison
of one endpoint: the billing paths differ, so read the token columns as
the harness measurement and the dollar column as indicative.

## Totals over the three sessions

| harness | model | input | cache read | cache write | output | turns | wall | passed | cost |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| janetcode | haiku | 141,476 | 2,472,188 | 0 | 26,974 | 148 | 346s | 3/3 | $0.542 |
| claudecode | haiku | 3,785 | 4,194,639 | 91,496 | 28,081 | 126 | 336s | 3/3 | $1.493 |
| janetcode | sonnet | 128,130 | 3,125,063 | 0 | 30,484 | 173 | 625s | 3/3 | $1.834 |
| claudecode | sonnet | 2,965 | 3,226,513 | 104,824 | 15,802 | 80 | 254s | 3/3 | $2.451 |

## Per session, context read (input + cache)

| task | janetcode haiku | Claude Code haiku | janetcode sonnet | Claude Code sonnet |
|---|---:|---:|---:|---:|
| 41-session-go-service | 1268k | 2342k | 1925k | 1283k |
| 42-session-records | 176k | 509k | 129k | 649k |
| 43-session-python-ledger | 1168k | 1346k | 1198k | 1296k |

## Reading it

Everything passed: 12 of 12 across the four cells, so this is about what
each harness spent to get there, not about capability.

**On haiku the difference is large and one-sided.** janetcode read 2.6M
tokens against 4.2M and cost $0.54 against $1.49 — 38% less context for
a third of the money. The per-session table says where it comes from: the
search-heavy task cost janetcode 176k against 509k, and the Go service
1.27M against 2.34M.

**On sonnet the token difference disappears.** 3.25M against 3.23M is a
dead heat, and the 25% cost gap is inside what the billing paths could
explain on their own. What is not a dead heat is how they got there:
Claude Code took 80 model calls and 253 seconds, janetcode 173 calls and
624 seconds, for half the output tokens. On this model Claude Code is
plainly the more decisive harness, and janetcode grinds.

**Neither harness is the reason these numbers are large.** The same three
sessions on qwen3.7-plus cost janetcode 985k in total. Here the two
code-writing sessions take 1.2-1.9M each while the search task stays
under 200k. Token cost is a property of the model and the harness
together, not of the harness — which also means the compaction results
measured on qwen do not transfer to these models unexamined.

**Caveats, in order of how much they matter.** One run per cell, on a
benchmark whose identical-configuration spread is 21%: the haiku gap is
far outside that, the sonnet gap is not. The billing paths differ
(OpenRouter list price against an Anthropic account), so the dollar
column compares two products, not two token counts. And Claude Code
carries a much larger fixed prompt — about 23k cache-read tokens for a
one-word reply, against janetcode's 1.8k — which is most of what the
haiku column is showing.

## Re-run

```sh
export OPENROUTER_API_KEY=...
go build -o bin/harnessbench ./cmd/harnessbench && make build
./bin/harnessbench --evals evals/session --filter session --harnesses janetcode \
  --model anthropic/claude-haiku-4.5 --base-url https://openrouter.ai/api/v1 \
  --out docs/benchmarks/harnesses/claude/janetcode-haiku
./bin/harnessbench --evals evals/session --filter session --harnesses claudecode \
  --claude-model haiku --out docs/benchmarks/harnesses/claude/claudecode-haiku
```
