# Cross-session memory: does a later session repeat the mistake?

2026-09-17. The claim on test: a session that already learned a project's
hidden rule spares the next session from rediscovering it. Measured on
`qwen/qwen3.7-flash` (OpenRouter), janetcode at master d4fe5c9, headless
`--yolo`, one process per session.

## Design

Five toy projects, each with a rule the code does not show, enforced by a
trusted `.janetcode/verify.json` gate that runs automatically after any
mutating turn and explains the rule when it fails:

| quirk | hidden rule |
|---|---|
| version-sync | VERSION file and version.go const must match |
| changelog | every change needs a CHANGELOG section for the version |
| manifest | MANIFEST must list every .sh file with its size |
| doc-comment | exported funcs need a doc comment starting with the name |
| lockstep | package.json version and core.js VERSION must match |

Per quirk, 3 repeats of a four-session protocol:

- **learn**: change the project (e.g. bump the version). The naive change
  trips the gate; the session discovers the rule from the failure and ends
  green. Mutating, so extraction and sleep both run.
- **warm**: a fresh session, same store — another change of the same kind.
- **coldprep / cold**: the same pair with the store reset before the second
  session, so nothing carries.

The measured mistake: the session-2 gate going red for the rule session 1
already learned. Raw sessions, commands and gate events:
`probe-results.json` (the two stale pre-restart rows for version-sync#0
learn/warm are superseded by the rerun's rows).

## Results

Trap coverage first, because everything conditions on it: the learn
session tripped the gate in only 9 of 15 groups — changelog and
doc-comment never fired at all (the model writes a changelog entry and
doc comments by convention, no failure to learn from). Those six groups
are null data and excluded from the effect count.

Of the 9 groups where the learn session did trip the gate, **the lesson
was captured into memory in 4** (all by extraction; sleep wrote no entry
anywhere in the matrix — see below). Conditioned on capture:

| group | warm red gates | cold red gates |
|---|---|---|
| lockstep#1 | 0 | 1 |
| lockstep#2 | 0 | 1 |
| manifest#2 | 0 | 1 |
| version-sync#0 | 0 | 1 |

**0/4 warm vs 4/4 cold.** Unconditioned (all 15 groups): warm 5 red, cold
8 red; tool calls 102 vs 94; wall 580s vs 590s. The mechanism, verified in
the session logs: not per-turn recall (zero memory bodies were attached in
the warm sessions) but the always-in-prompt memory index — the rule's
one-line description was in the system prompt and the model simply didn't
make the mistake.

## What this supports and what it doesn't

- Supported: when a cross-session lesson is captured, it prevents the
  repeated mistake, at the price of one index line. That is the claim.
- Not supported: any unconditional effect. The bottleneck is entirely the
  capture step: 4/9 of trap-sprung learns left a memory. Where nothing was
  captured, warm and cold are the same coin flip.
- Sleep (the ledger consolidation pass) produced zero entries in 60
  sessions on this model. Its prompt names gate-failure lessons as the
  highest-value record and flash still answered NONE every time;
  extraction over prose did all the capturing. Either the pass needs a
  stronger model, or its input should be the failing gate events alone
  rather than a whole-session record. Recorded as a real negative result,
  not tuned away.
- One warm run (manifest#1) went sideways without a captured memory and
  spent 15 tool calls — the no-memory warm/cold spread has outliers in
  both directions.

## Bugs the probe caught in the harness (fixed in d4fe5c9)

- The lenient extractor parser turned pretty-printed JSON into one garbage
  memory entry per line (literal `"name": "..."` entries in the store).
  ParseExtracted now rejoins multi-line objects first.
- Extraction and sleep prompts never named the highest-value lesson class;
  a pilot session that tripped a gate and recovered recorded nothing. Both
  prompts now say: a check that failed and later passed teaches the rule
  the check enforces.

Cost of the whole matrix: ~$0.03 at list prices, ~45 minutes.

## Reproduce

The probe driver (project generators, protocol, metrics) is not committed —
it writes scratch projects and seeds a trusted verify gate into an isolated
HOME. If it earns keep, it graduates into evals/ as a `memory` tag; the
quirk designs above are the spec.
