# Memory: where it goes next

Design notes on the memory system, after a read of the current agent-memory
literature (MemGPT, Generative Agents, A-MEM, HippoRAG, Zep/Graphiti, Mem0,
MemoryOS, ReadAgent, sleep-time compute) against what internal/memory and
the agent loop already do. Sources at the bottom.

## Where the literature lands

Read for mechanism, the systems converge on the same handful of moves:

- **Invest in the write path.** A-MEM enriches each note (keywords, gist,
  links) when it is written; Generative Agents score importance 1–10 at
  write time. Both make the read path a cheap lookup. Reading quality is
  bounded by writing quality.
- **Invalidate, never delete.** Zep stamps every fact with a validity
  interval and closes contradicted facts instead of dropping them;
  Mem0's ADD/UPDATE/DELETE/NOOP decision does the same flat. Stale
  knowledge (moved files, reversed decisions, old versions) is the
  normal case, not an edge case.
- **Rank by recency × importance × relevance.** Generative Agents' sum;
  MemoryOS's heat score; ACT-R's base-level activation is the closed
  form of the same idea: B = ln Σ age^(-0.5) over past uses, so
  frequency and recency fall out of one expression and retrieval itself
  strengthens a memory (spaced repetition).
- **Consolidate in the background, on a trigger, with citations.**
  Generative Agents fire reflection when accumulated importance crosses
  a threshold, and the reflections point at the records they came from;
  Letta's sleep-time compute moves the same work off the turn path
  entirely. Citations matter: Generative Agents' own failure analysis is
  memories drifting from their evidence — which has already bitten here
  once (harness text extracted as user feedback).
- **Let retrieval ride structure that already exists.** HippoRAG's
  spreading activation over an entity graph; ReadAgent's
  paginate-gist-expand. janetcode already owns both structures: repomap
  is the graph, and archive/expand_output is gist-then-expand for tool
  output.

Two caveats from the numbers, stated so the design stays honest:

- On Zep's and Mem0's own benchmarks, full context still beats memory
  where it fits (98.0 vs 94.8 on DMR; J=72.9 vs 66.9 on LOCOMO). Memory
  buys cost, latency and horizon, not accuracy. That is the same fight
  as the leanness work, not a rival to it.
- Mem0 shows a flat fact store with a sane merge decision matches the
  graph variants except on temporal questions. No graph infrastructure
  here; flat files stay.

## What is already here

Typed, scoped entries as flat Markdown files; Jaccard merge at write;
secret-shaped entries refused; a guarded extraction call after mutating
turns (harness text quarantined, project scope forced); an 8 KB index in
the frozen prefix plus up to 4 recalled bodies a turn; attention-order
pruning and a typed working record across compaction; /dream as manual,
user-exempt consolidation; optional embeddings when the provider offers
them.

The gaps, in the order the plan uses them:

1. Recall ranking is flat word overlap; `Uses` only feeds eviction, not
   ranking (memory.go:510).
2. The trajectory ledger and the critic record every refusal, verdict,
   retry and compaction — and nothing learns from them. The predictor
   trains only on eval runs because those are the only labelled
   failures.
3. The extractor's window is the last 14 messages; durable knowledge
   from earlier in a long turn never reaches it.
4. Consolidation is manual (/dream) and blind to importance.
5. Stale facts have no representation: `replaces` is silently dropped
   unless the target exists and is project scope.
6. Entries do not know what they are about — no link to the files or
   decisions they concern, so recall depends on the user phrasing a
   query that overlaps.
7. User-sourced entries can be evicted by the 200-cap; only /dream
   protects them.

## The plan: experiential memory

Three stages, each independently shippable, each behind an eval switch,
each measurable with --eval-compare. The direction: memory stops being a
notebook of facts and becomes a loop that turns experience into
judgement — the ledger already records the experience.

### Stage 1 — activation-ranked retrieval (read path; landed on master_mem)

Replace word-overlap-only scoring with:

    score = lexical + λ · activation + type prior
    activation(m) = ln( Σ over recorded uses j of age_days(j)^(-0.5) )

Uses and timestamps already exist on every entry and Touch already fires
on recall; a use-history ring (last ~20 timestamps) is one frontmatter
line. The type prior (user/feedback outrank auto project facts at equal
activation) absorbs today's strongRecall limbo into a smooth term. Same
pass: user-sourced entries exempt from the 200-cap trim, and /remember
refreshes the index immediately instead of waiting for compaction.

### Stage 2 — provenance and validity (data model)

Two optional frontmatter fields:

    evidence: ["<session-id>#<seq>", ...]
    valid: {from: <date>, until: <date|null>, superseded_by: <name|null>}

Extraction already returns typed JSON lines; it starts asking for the
event ids it read, and the harness fills them in when the model omits
them — it knows what it showed. `replaces` stops being a silent no-op
and closes the old entry instead (valid.until + superseded_by; the file
stays). Recalled entries render with their validity window, which is
what Zep's ablations credit for the temporal-reasoning win. Additive:
old files parse unchanged.

### Stage 3 — sleep consolidation over the ledger (write path)

A background pass — cron or on goal completion, never in the turn path —
that reads structured session ledgers (critic warnings with their top-3
features, verdicts, refusals, compactions, and the Decisions/RuledOut/
Facts sections working memory already distilled) rather than chat text.
One call per session, written through the same guarded Apply path as
extraction, with mandatory evidence ids validated against the ledger.
Because the ledger records touched files, each entry gets a
`concerns: [paths]` line for free; TurnPrep then recalls entries whose
concerns intersect the files the current turn is touching — the query
is implicit in the work, which is the move a chat memory cannot make.

Side effect worth naming: sleep sessions produce labelled real
trajectories (goal completed/blocked + feature vector), which is the
training set the trajectory critic is currently missing.

Deliberately out: rewriting old entries (A-MEM's evolution — weakest
evidence, silent mutation of history), graph infrastructure, mandatory
embeddings, per-message LLM calls, per-query anticipation. Global scope
still requires the user; the 2026-09-10 contamination incident is why.

## Measurement

Each stage: multistep set (51–62) ×3 with --eval-compare against the
previous stage. Pass-rate intervals include zero at this set size, so
the columns that can resolve are context per solved task and cost; stage
3 adds the session set (41–43), since cross-session recall is its actual
claim, plus a purpose-built probe: a two-session task where session 2
needs a fact only session 1's ledger contains.

## Risks

- Sleep cost is one call per session; the trigger respects /budget.
- λ and the type prior are guesses until the recall fixtures pin them.
- Evidence ids the extractor cites must be validated against the ledger,
  or the citation is fiction.
- Memory does not raise pass rate where context already fits. The claim
  to defend is lower context per solved task, fewer repeated mistakes
  across sessions, and a trainable critic.

## Sources

- MemGPT — arXiv:2310.08560 (agent-editable core block; memory-pressure
  warning; append-only recall)
- Generative Agents — arXiv:2304.03442 (importance-at-write;
  recency+importance+relevance; threshold-triggered reflection with
  citations; memory-hallucination failure analysis)
- A-MEM — arXiv:2502.12110 (write-time enrichment and links; evolution
  ablation)
- HippoRAG — arXiv:2405.14831 (spreading activation over an entity
  graph; node specificity)
- Zep/Graphiti — arXiv:2501.13956 (bi-temporal facts;
  invalidate-not-delete; the full-context caveat)
- Mem0 — arXiv:2504.19413 (ADD/UPDATE/DELETE/NOOP per candidate; flat
  matches graph except temporal)
- MemoryOS — arXiv:2506.06326 (tiered store; heat eviction)
- ReadAgent — arXiv:2402.09727 (structural pagination; gist;
  in-place re-expansion)
- Sleep-time compute — arXiv:2504.13171 (offline learned context; gains
  moderate by query predictability)
- Survey — arXiv:2404.13501 (sources/forms/operations taxonomy)
- ACT-R base-level activation B = ln Σ t^(-0.5); Ebbinghaus R = e^(-t/S)
  with stability growing on rehearsal
