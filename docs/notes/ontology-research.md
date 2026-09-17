# Ontology for memory: what paid and what did not (2026-09-17)

Branch `master_onto` tried a typed vocabulary package (`internal/onto`)
plus two uses of it: harvesting a verify-fail then verify-pass into a
project rule without a model call, and admitting memory entries when a
turn's files, expanded through the repository map, overlapped their
concerns by path segment. Both were replayed against the 154 real
sessions on the development box before merging. Neither survived.

## Measured

| question | result |
|---|---|
| bash fail-then-pass pairs of the same command, 154 sessions | 8 |
| of those, a hidden project rule (not the task, not a refusal) | 0 |
| later sessions a harvested rule would have saved | 0 |
| later sessions where graph expansion found an earlier session's files that exact path match missed | 0 of 55 |
| later sessions where path-segment matching fired beyond exact match | 2 of 55, both two localhost URLs sharing a port |
| later sessions already covered by exact file overlap | 48 of 55 |

The harvested "rules" were named after scratch paths and described by
the first line of test-runner output (`=== RUN TestSumTo`, a pytest
banner). On this repo the graph expansion hit its 32-file cap on every
session. No session anywhere had a gate event: gate logging landed
2026-09-14 and no project configures `.janetcode/verify.json`, so the
hidden-invariant probe remains the only place a gate pair has fired.

## What the literature actually claims

- A structural code index inside one harness, causally ablated: file
  localization 44% to 85%, resolve 42% to 50%, no cost penalty
  (arXiv 2606.22417). That is the repository map, which janetcode has.
- An OWL ontology over project memory (MOOSEDev, arXiv 2608.13662)
  wins near-categorically on set completeness, negation and supersession
  traversal, and ties a flat vector store on relevance retrieval; the
  authors state the structured advantage "did not compound" with store
  size. Its value is disciplined typed capture and supersession, not
  graph recall.
- MemCoder (arXiv 2603.13258) lifts DeepSeek-V3.2 from 68.4% to 77.8%
  resolved by structuring commit history into memory and crystallising
  verification feedback into entries, with a model in the loop.

## What was kept

The schema janetcode already has (five types, `concerns`, `evidence`,
`replaces`, `superseded_by`, `valid_until`) is the ontology. What
landed is code that asks structural questions of it:

- `sleepfold` records a passing gate beside a failing one, and the sleep
  record folds the pair into one line: `[9] (check make test) passed
  after failing at [7]`. The model still decides whether that is a rule.
- The sleep prompt lists open entries whose concerns name a file the
  session changed under their own heading, so a correction names what it
  `replaces`. Exact paths only.
- `/memory about <path>`, `/memory history <name>`, `/memory current`:
  exact answers from frontmatter, no ranking.

Not kept: `internal/onto`, graph-expanded recall, segment matching, any
rule writer that bypasses the model, an `onto` switch.

## Still open

Whether the paired record line moves sleep off its zero-entry result on
flash, and whether capture rises above 4/9 on the probe's nine
trap-sprung groups. The probe driver is not committed; rebuilding it is
the next step before any further memory claim.
