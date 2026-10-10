# janetcode: current engineering state (as of 2026-10-10, branch research/frontier-ideas @ d534fe31)

Scope: local codebase research only. Sources are repo files (paths relative to /home/driver/wspace/janetcode). Nothing under reports/ or research_notes/ (other than this file) was read. No builds or model calls were run.

## Q1. Architecture: what each subsystem is and where it lives

### Takeaway
janetcode is a single Go binary (bubbletea TUI, `--web`, headless `-p`, ACP, Go SDK over ACP) whose agent loop (`internal/agent`, ~100 files) is wrapped in an unusually large set of deterministic, model-free harness checks (verdicts, gates, loop/burst detection, coaching, provenance), with every model-dependent "smart" layer (Jev, critic, advisor, cascade, step routing) shipped opt-in and default-off after measurement.

### Cited Findings
**Core loop and providers**
- `internal/llm` is the provider-neutral core: Message/Block, ToolDef, Request, Event, `Provider.Stream(ctx, req, emit)`; `internal/agent.Agent.Run` is the loop; `Approver` is the permission hook — [PROJECT-STATE.md](PROJECT-STATE.md) "Architecture Notes".
- Providers: Anthropic via official SDK; OpenAI-compatible hand-rolled to tolerate dialects (Qwen, DeepSeek, vLLM, Ollama, OpenRouter) and learn quirks from 400s at runtime; native Gemini (keeps thought signatures) and OpenAI Responses (replays encrypted reasoning) share `internal/llm/httpstream` retry/key-rotation loop; `llm.KeyRing` per-key circuit breakers; one shared `llm.Retrier` — [PROJECT-STATE.md](PROJECT-STATE.md); [CHANGELOG.md](CHANGELOG.md) "Parity with qwengo"; [docs/notes/history.md](docs/notes/history.md) "Review-and-fix round 2".
- History is append-only (preserved thinking rejects edited history); a turn's tool results go back in one user message; history stays provider-valid on every return path incl. cancellation — [PROJECT-STATE.md](PROJECT-STATE.md) "Key Decisions".
- Read-only tools at the start of a batch run concurrently; from the first mutating call on, calls run serially in model order — [README.md](README.md) "What it can do".
- Stream watchdog kills stalled streams; a stream that died without a terminal frame is retried as one fresh request (`EventStreamDiscard`); prose-written tool calls are recovered; quota-exhausted model falls back to a spare (`agent/fallback.go`); malformed tool args mended/wrapped (`llm.InvalidArguments`) — [docs/notes/history.md](docs/notes/history.md) (qwen-code review, Solai round); [PROJECT-STATE.md](PROJECT-STATE.md).
- Request integrity check before every call (request text present, history never shrinks unexplained) — `internal/agent/integrity.go`; prompt-cache prefix kept append-only — `internal/agent/prefix.go`.

**Tools**
- ~25 tools: read_file, write_file, edit_file (exact string, line range or content anchor; multi-edit; `then_run`), list_dir, glob, grep, bash, fetch_url, web_search, repo_map, symbols (LSP), diagnostics, expand_output, execute_code (Python script calling tools), run_code (JSON list of calls), workflow, task (subagents, optionally other model, worktree-isolated edit subagents), terminal, job, todo, ask, inspect, memory, skill, goal_status, tool_search — [README.md](README.md).
- `lean_tools` on by default (tools beyond a core are deferred and loadable via `tool_search`); headless sends only 5 core tools (read/write/edit/bash/tool_search), 25 KB→6.5 KB schemas — [docs/benchmarks/toolsurface/REPORT.md](docs/benchmarks/toolsurface/REPORT.md); [CHANGELOG.md](CHANGELOG.md) "Lean execution".
- Tool results >24 KB archived per project (`agent/archive.go`), retrievable by `expand_output`; per-tool inline budgets (bash 12 KB, grep 8 KB, else 24 KB); head+tail truncation — [docs/notes/history.md](docs/notes/history.md) sixth/ninth round.
- `tool.Seen` file-hash conflict refusal ("file changed since read"); BOM/line-ending/charset preservation — [docs/notes/history.md](docs/notes/history.md).

**Permissions, safety, sandboxing**
- `internal/perm`: allow/deny/ask rules, typed scopes, managed policy (`internal/managed`), `r` at prompt saves `bash(go test *)`-style rules; project trust levels (`instructions` vs `full`) — [PROJECT-STATE.md](PROJECT-STATE.md) "Key Decisions"; [docs/notes/history.md](docs/notes/history.md) eighth round.
- `internal/shell` (4,849 lines): command classifier (read-only / workspace-escape / catastrophic; catastrophic refused even in yolo; auto-approve never skips the guard), env scrubbing of credentials by shape, process-group supervision — `internal/shell` package doc; [PROJECT-STATE.md](PROJECT-STATE.md).
- Sandbox backends: bubblewrap (preferred on Linux), Landlock, docker, macOS seatbelt; `internal/egress` per-turn report of what left the machine + host policy via filtering proxy — [CHANGELOG.md](CHANGELOG.md) "Parity with qwengo"; `internal/egress` package doc.
- Credential redaction at a single choke point `Agent.runSafely` — [PROJECT-STATE.md](PROJECT-STATE.md).
- Hash-chained audit log per session (`--audit`) — git log `b9ddc105`.

**Verification gates and "what actually happened"**
- `internal/verify`: `.janetcode/verify.json` (or project-type defaults) runs when a turn that mutated ends; failures fed back up to 3 rounds; optional mutation testing (`internal/mutate`: flip operators on changed lines in a throwaway worktree, survivors fail the gate) — `internal/verify`, `internal/mutate` package docs; [docs/notes/history.md](docs/notes/history.md) eighth round.
- `verify_baseline` (on by default since 2026-10-02): checks run before the first edit so the gate only sends back failures the turn caused — `internal/agent/baseline.go`; [docs/benchmarks/orient-baseline/REPORT.md](docs/benchmarks/orient-baseline/REPORT.md).
- `agent/verdict.go` (841 lines): computes from events what the turn demonstrated (ran tests? edited after last check?) vs what the model claims; reports, never blocks; `Overclaimed()`, `StaleCheck()` nudges; `internal/gaming` flags cheap signs of tests made green without behaviour (reports only) — file headers.
- `/accept` mode (`internal/accept`, 1,038 lines): "acceptance tests you approve first, that the model can't weaken" — package doc.
- Spec review (headless): a green turn that changed files is sent back once to re-read the brief requirement by requirement, max 3 tool steps, no new checks — `internal/agent/specreview.go`.
- Test guard (headless): first write to a test file is refused — `internal/agent/testguard.go`.
- Coach (`agent/coach.go`): failed edit on unread file inlines the file; second failure escalates; `agent/syntax.go`: syntax check + covering-test pointer after edits — file headers.
- Post-edit LSP diagnostics now run beside the turn (`agent/postdiag.go`), errors block "done" at most twice — [CHANGELOG.md](CHANGELOG.md).

**Loop control / runaway prevention**
- Loop ladder: nudge at 3 identical calls, `ErrStuck` at 5, named loop taxonomy (`agent/loop.go`); burst cap: a reply starting >24 calls is cut, and a reply is ended at its first exact repeat (`agent/burst.go`); plan budget: headless reply reasoning >32k chars without acting is sent back, up to 3x/turn (`agent/planbudget.go`); cut-off-at-output-limit replies double the limit for the turn (16k→32k→64k) — [CHANGELOG.md](CHANGELOG.md) "Lean execution"; [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).
- Run limits: `--max-cost` (exit 55), `--max-tool-calls` (57), `--max-wall-time` (56), `--max-turns` — [CHANGELOG.md](CHANGELOG.md).

**Context management**
- Compaction: default `compact_at` 80k (window×¾ capped 200k); `Agent.Prune` tiers first (stub superseded reads, trim old results, tool-aware placeholders, archive), then a typed working-memory record (`agent/working.go`) instead of a prose handoff; keeps verbatim tail (≤6 msgs, ≤12k tokens); "Already read: / Already changed by you:" paths appended mechanically — [docs/notes/history.md](docs/notes/history.md); [docs/benchmarks/compaction/PRESSURE.md](docs/benchmarks/compaction/PRESSURE.md).
- NEED protocol ("epistemic faulting"): after compaction the model must call `expand_output` or emit `NEED: <query>`; harness resolves against archive, up to 2 rounds — `internal/agent/need.go`.
- Small repo (≤40 files, ≤32 KB) inlined whole in the first message instead of the repo map — [CHANGELOG.md](CHANGELOG.md) "Lean execution".
- Oversized single tool results guarded (`agent/oversized.go`); `windowFromError` lowers threshold on overflow.

**Repo map, LSP, impact**
- `internal/repomap`: file graph (Go via go/parser, others regex; optional tree-sitter with `-tags treesitter`), PageRank personalised by request pull, ~1,500 tokens attached via `Prepare`; precision fix (request word pull divided by match count; sqrt scaling past 1,000 files; 10k-file tiered indexing for kubernetes-sized trees) — `internal/repomap` package doc; [docs/notes/large-repos.md](docs/notes/large-repos.md); [docs/notes/history.md](docs/notes/history.md).
- `internal/lsp`: minimal stdio JSON-RPC client, lazy per-extension servers (gopls/pyright/tsserver/rust-analyzer/clangd) — package doc.
- `internal/impact`: before a mutating edit is approved, names symbols whose definitions the edit touches, reference counts, covering tests, verify commands that will run (LSP if available, else repomap marked Partial) — package doc.

**Memory, skills, rules**
- `internal/memory` (2,305 lines): one md file per entry, 5 types (user/feedback/project/decision/reference), `concerns/evidence/replaces/superseded_by/valid_until`; index (8 KB) always in prompt; lexical + optional embedding recall (≥4 score, ≤4 bodies/turn); extraction after mutating/feedback turns; "sleep" consolidation (`internal/sleepfold`) — [docs/notes/history.md](docs/notes/history.md) seventh round; [docs/notes/ontology-research.md](docs/notes/ontology-research.md).
- Headless memory extraction/consolidation now opt-in (were two full-history calls after every run) — [CHANGELOG.md](CHANGELOG.md).
- `internal/skill`: lexical `Select` (score≥3, ≤5/turn); Claude-compatible skill dirs; `internal/rules` path-scoped rules; plugins bundle commands/skills/agents/hooks/MCP — [docs/notes/history.md](docs/notes/history.md); git log `2b571a0e`.

**MCP, hooks, ACP**
- `internal/mcp`: stdio + Streamable HTTP/SSE, resources/prompts, OAuth (PKCE, RFC 9207/9728/8414/7591), >6-tool servers deferred; `internal/mcpserve` exposes janetcode as MCP server — package docs; [CHANGELOG.md](CHANGELOG.md).
- `internal/hook`: pre_tool/post_tool/pre_compact/stop; JSON input rewrite; project hooks need trust — [docs/notes/history.md](docs/notes/history.md).

**Critic, plan, trajectory, why**
- `internal/trajectory` folds the event log into a per-turn/step ledger; `internal/critic` holds the fitted logistic predictor (scores every 5 tool calls, notes <35%) and `/why`; predictor never saved because it never beat majority baseline (86% vs 91%; 54% vs 77%; 76% vs 76%) — package docs; [docs/notes/history.md](docs/notes/history.md).
- Plan check (repomap/plan.go) flags backticked paths/symbols the repo lacks; `/plan` read-only mode with approval; `internal/plan` holds plan file + user comments — [docs/notes/history.md](docs/notes/history.md); `internal/plan` doc.
- `--session-profile` classifies recorded calls explore/edit/verify, measures stale input; `agent.WasteLabels` labels wasted calls by construction (errored/denied, loop, dead tail) — [docs/notes/history.md](docs/notes/history.md) "Context curator round".

**Provenance, proof, undo, replay**
- `internal/change` + `agent/provenance.go`: Before/After per file per tool call with the model's intent, own Myers diff; `/changes`, `/blame`, `/undo n` — [docs/notes/history.md](docs/notes/history.md).
- `internal/proof`: attaches machine-readable evidence to a git commit as a git note (session, turn, stated intent per changed file, plus what the harness observed demonstrating the work) — package doc.
- `internal/snapshot`: git work-tree snapshots as hidden commits (`refs/janetcode/cp/`) so `/undo` reverts shell-made changes; shadow store outside git; Touched manifest so undo never clobbers post-turn edits — package doc; [docs/notes/history.md](docs/notes/history.md).
- `internal/provenance`: decides whether a model call was served by the model asked for (model field, gateway provider, response-id/tool-call-id formats); evals mark MISMATCH/INFRA — package doc; [CHANGELOG.md](CHANGELOG.md) "Borrowed and measured".
- `internal/replay`: a recorded session becomes a provider (cassette); `internal/whatif`: counterfactual replay — rebuild tree before turn N by replaying 1..N-1 against recorded replies, no tokens — package docs.
- `internal/recipe`: crystallises a turn that worked into a replayable program of real tool calls, run with no model call through the normal approval pipeline — package doc.
- Durable turns: crash-recognisable turn ends; `/recover` reruns reads, never repeats mutations, describes mutating calls' effect from disk — [docs/notes/durable-turns.md](docs/notes/durable-turns.md).
- `--dry-run` (`agent/dryrun.go`): model reasons, reads and plans against a preview.

**Model slots, routing, Jev**
- One resolver `config.Slot(name)` for cheap/strong/advisor/vision slots; step routing `route_cheap` (rule-based, not learned; mechanical steps → cheap model, tainted at first edit/failure; refuses cross-provider above 20k context); cascade (`strong_model`+`escalate_after`, triggers on gate failure or 60% of max_iterations); advisor (`advisor_mode: always`, forced consult injected as text, ≤2/turn); per-call evidence of which slot served it and why — `internal/agent/steproute.go`, `escalate.go`, `advisorforce.go`; [docs/notes/history.md](docs/notes/history.md) "Model slots", "Routing evidence".
- `internal/jev`: fail-open client to TypeSafe System One (typed Noul/Choice/Score answers with confidences), 800 ms deadline, never retried, half-open circuit breaker; consumers: verify-failure triage (`agent/jevtriage.go`), intake classification (log-only), intake routing (explain-only), session outcome (log-only), offline annotation, stuck replay — all default OFF — [docs/notes/history.md](docs/notes/history.md); [docs/notes/jev.md](docs/notes/jev.md).

**Evals and benchmarking infra**
- `internal/eval` (4,674 lines): JSON tasks with shell checks, hidden held-out files never written to workdir (`acceptance/` overlay via `gate.sh`), `--eval-runs/--eval-compare` with paired bootstrap, Wilson CI, Bonferroni across gates, sign-test fallback, MISMATCH/INFRA exclusion; switches via `--eval-off` — [docs/notes/history.md](docs/notes/history.md).
- Eval sets: 01–20 easy, 21–26 hard, 31–35 large, 41–43 session, 51–62 multistep, explore/61, fanout, gated 71–73, hard 81–86 (mixai ports), hidden 91–98 (incl. controls 97/98) — `evals/`.
- `cmd/harnessbench` runs janetcode, pi, hermes-agent, qwencode, openclaw, Claude Code (via `cmd/modelproxy`); effbench arm in `docs/benchmarks/effbench/run.py` — [docs/benchmarks/samemodel/REPORT.md](docs/benchmarks/samemodel/REPORT.md); [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).

**Self-awareness/UX**
- `internal/feature` catalogue generated from commands/flags/tools/config with tests forcing documentation; `internal/hints` one-shot contextual hints; `internal/inspect` + `introspect`/`inspect` tool; `/why` — [docs/notes/history.md](docs/notes/history.md) "Self-aware harness".

### Inferences
- The architecture's centre of gravity is "deterministic harness observes and corrects the model" rather than "more model calls". Nearly every model-in-the-loop helper (critic, Jev, advisor, cascade, routing, orient) exists but is off by default.
- Headless and interactive modes have diverged substantially: headless has its own prompt (`prompt.Headless`), tool core, test guard, spec review and plan budget. Benchmark wins are on the headless path.

### Gaps
- I did not read every agent file; line counts and package docs were used for the less central packages (e.g. `cron`, `gitflow`, `voice`, `cua`).
- `internal/minimal` (eval agent) differs from headless (no full tool surface); which eval numbers used which driver is per-report.

## Q2. Experiments run, what they showed, and ideas tried and rejected

### Takeaway
The project runs disciplined A/Bs with paired bootstraps and records negatives. Headline: effbench r13 (MiMo V2.6 Flash, 16 tasks × 2 seeds) janetcode 30/32 valid vs pi 26/32, 4% faster, 20% fewer calls, 30% less output. Many "smart" ideas (compiled loop, receipts, ontology, mid-turn Jev, intent field, orient advice, cascade, critic predictor) were measured and rejected or left off.

### Cited Findings
**Headline benchmarks**
- effbench r13 (`5c3ed1a2`): janetcode 30/32 valid, 12,792 s, 541 calls, 12.18M input, 876k fresh, 900k output, 0 tamper; pi 27/32 (26 valid), 13,323 s, 675 calls, 10.75M input. Over r12+r13 janetcode 60/64 valid, pi 55/64. janetcode total input is 13% higher (cached context the review rereads) — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).
- Over r8–r10 (96 runs each) both had 82 valid passes; janetcode 14% less time, 32% fewer calls, 42% less output — i.e. before spec review the two were tied on passes — same source.
- effbench r1 baseline (`13fd1857`): janetcode 13/15 vs pi 16/16; every change after came from a losing transcript — same source, "What got janetcode there" table.
- Held-out r5: janetcode 10/16 vs pi 11/16; calls/input half of pi's — same source.
- Eleven models × five harnesses × 3 runs (3,300 runs, easy set 01–20): janetcode 651/660 best, lowest cost/solved (0.18¢ vs hermes 0.25¢, pi 0.47¢) — [README.md](README.md) "What has been measured".
- Same-model vs Claude Code (qwen3.7-plus): janetcode 87/87 vs 77/87, 2.4–6.2× less context; Claude Code misses mostly files written outside task dir. On mercury-2.5 session set: janetcode 3/3 vs 2/3, 47% fewer input tokens — [README.md](README.md); [docs/benchmarks/samemodel/REPORT.md](docs/benchmarks/samemodel/REPORT.md).
- On sonnet (Claude models), Claude Code reached same token totals in 80 model calls vs janetcode's 173; earlier "1.75× slower on sonnet" did not reproduce with model held constant — [README.md](README.md); [docs/benchmarks/samemodel/REPORT.md](docs/benchmarks/samemodel/REPORT.md).
- Lean execution A/B (MiMo, Novita-only): calls −41%, wall −28%, tool calls −53%, cost −12%, passes level; inlining small repo carries most of it (inlining off → 6.9 calls, no better than old) — [docs/benchmarks/lean-ab/REPORT.md](docs/benchmarks/lean-ab/REPORT.md).

**Shipped because measured positive**
- `verify_baseline`: held-out controls 0/4 → 5/5 (Fisher p≈0.008), context/solved ×0.54 on hidden; on by default — [docs/benchmarks/orient-baseline/REPORT.md](docs/benchmarks/orient-baseline/REPORT.md).
- `lean_tools` (4 vs 9 tools): context ×1.23 [1.10,1.44] cost for extra tools on qwen, replicated ×1.23 on mercury (234 runs); neutral on large set — [docs/benchmarks/toolsurface/REPORT.md](docs/benchmarks/toolsurface/REPORT.md).
- Line-range edits: cost ×0.90 [0.84,0.97] on mercury 144 runs (null on qwen at 27 runs) — [docs/benchmarks/lineedit/REPORT.md](docs/benchmarks/lineedit/REPORT.md).
- `execute_code` (after positional-stub fix): cost ×0.88 [0.75,0.99] on fanout, sonnet; before fix it was ×0.74 context in favour of NOT having it (25% scripts crashed on `read_file()` signature) — [docs/benchmarks/script/REPORT.md](docs/benchmarks/script/REPORT.md).
- Plan budget: output −21% at no pass cost (r7); burst cap and repeat cut fixed 120-call blowups (r9→r10); spec review fixed hidden-test misses but cost ~150 s/run until made read-only with 3 steps (r11→r13) — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).
- Repo-map precision@5 0.00 → 0.40 (ceiling) deterministically — [docs/notes/history.md](docs/notes/history.md) "Model-mixing round".
- Compaction "Already read:" mechanical path list: 7/18 → 9/18 under forced 6k pressure (direction only) — [docs/benchmarks/compaction/PRESSURE.md](docs/benchmarks/compaction/PRESSURE.md).
- Memory: when a lesson is captured, warm sessions 0/4 red vs cold 4/4 — effect carried by the always-in-prompt index line, not per-turn recall — [docs/benchmarks/memory/REPORT.md](docs/benchmarks/memory/REPORT.md).

**REJECTED / FAILED / LEFT OFF (do not re-propose without addressing the stated reason)**
- **Compiled agent loop / explore-then-compile (`run_plan`, JIT)**: offline depgraph said 36–59% of model calls avoidable, but live A/B: models barely used it (1/24 MiMo runs), saved nothing on deepseek (6.1→5.9 calls) and MiMo (C/A calls 1.15); batching prompt ~10% at best, CI includes none. First headline (73% cheaper) was an artefact of broker serving Kimi. Code on branch `development_jit`. Untested on a model that actually doesn't batch — [docs/benchmarks/jit/REPORT.md](docs/benchmarks/jit/REPORT.md).
- **Evidence-Preserving Reducer / receipts (SoL-Pi EPR)** for long tool output: mechanism worked (72 KB→912 B, byte-exact quotes) but wall +43%, input ~2×, cost +62% in janetcode's short-session regime; reverted. Correct regime would be long sessions where receipts recur — [docs/notes/jev.md](docs/notes/jev.md) "Evidence-Preserving Reducer".
- **Ontology / graph-expanded memory recall / model-free rule harvesting**: replay on 154 sessions — 0 rules, 0 saves, 0 recall beyond exact path match (48/55 already covered by exact overlap); `internal/onto` cut — [docs/notes/ontology-research.md](docs/notes/ontology-research.md).
- **Mid-turn Jev consumer (stuck/loop/next-call-will-fail)**: rejected twice. On qwen 1.7% wasted calls, zero loops; on devstral 6.2% waste but `stuck` recall 0.07; trigger itself (same tool+path twice) is the free deterministic signal; ~2.7 s added/run. Revisit only with a harness-side "did the state change" check — [PROJECT-STATE.md](PROJECT-STATE.md) "Known Gaps"; [docs/notes/history.md](docs/notes/history.md).
- **Jev triage variants**: skip-mass scoring reverted (costly skip on patch_caused); structured object state instead of prose reverted (29–30/35 vs 33/35). Jev overall: savings 15–20% fewer calls but correctness delta in noise; all decision flags OFF; marginal value = zero on patch-caused path, all of it on environmental failures (which `verify_baseline` now also covers deterministically) — [docs/notes/jev.md](docs/notes/jev.md); [docs/notes/history.md](docs/notes/history.md).
- **Jev Phase 2 (trajectory success_likelihood)** skipped: no rubric, no corpus; **plan-drift (`on_goal`)** did not separate on 28- or 80-row corpora; **subagent delegation decision**: no hook point exists; **hint suppression, advisory security second opinion**: not built (cost/benefit, never in permission path) — [docs/notes/jev.md](docs/notes/jev.md) "The rest of the list".
- **83-item Jev use-case brainstorm**: 66 cut — 24 because deterministic code does it exactly, 17 security/authority, 11 arithmetic/counting, 8 per-call latency, 6 no ground truth — [docs/notes/jev.md](docs/notes/jev.md).
- **Cascade (cheap→strong on gate failure)**: on hard set, strong model solo was cheapest and fastest (qwen3.7-plus 9/9 $0.053 vs devstral base $0.345); live cascade 7/9 vs cheap solo 8/9 at +22% cost/solved; ships default-off — [docs/notes/history.md](docs/notes/history.md); [docs/notes/model-mix.md](docs/notes/model-mix.md).
- **Difficulty-judge routing / advisor-as-tool / senior+junior pairs** (from mixai prior): advisor and fusion tools go unused; difficulty judge over-routes; hardcoded promotion misfires — [docs/notes/model-mix.md](docs/notes/model-mix.md).
- **Learned routing (item 6)**: not built — corpus is one-class (8/9, 11/12, 18/18) — [docs/notes/history.md](docs/notes/history.md).
- **Intake routing**: shipped acting but restricted to "explain" turns only (review cut after a costly route); 0/29 costly, 3/12 recall — [docs/notes/history.md](docs/notes/history.md).
- **Orient advice (turn-start advisor reads likely files: LOOK/WATCH FOR/CHECK)** and self-orientation: no headroom on DeepSeek V4.1 Flash; stays off. Advisor-always had one positive n=3 signal (3/3 vs 2/3) — [docs/benchmarks/orient-baseline/REPORT.md](docs/benchmarks/orient-baseline/REPORT.md); [docs/notes/jev.md](docs/notes/jev.md).
- **Trajectory critic predictor**: never beat majority baseline on any corpus; not saved — [docs/notes/history.md](docs/notes/history.md).
- **Compaction-time pruning consumer (item 3)**: zero value because no eval ever compacts at realistic thresholds (peak ~26k vs 80k) — [docs/notes/jev.md](docs/notes/jev.md).
- **Required per-tool-call `intent` field**: context ×1.21 [1.15,1.26], output ×1.25, cost ×1.10 for unmeasured benefit; now off by default; `accept_large_output` schema field removed — [docs/benchmarks/intent/REPORT.md](docs/benchmarks/intent/REPORT.md).
- **Prompt trims**: a 440-byte cut to harness section reverted (59/60, +7% tool calls) — "the prompt is exactly as sensitive as the benchmarks say" — [docs/notes/history.md](docs/notes/history.md) "Leanness audit".
- **Coercive done-gates, LLM reviewers/planners, signed audit logs, plan DAGs** deliberately skipped from hero_shrimp (its data said net-negative); "dream" consolidation, plan-registry slugs, inert LLM reviewer not taken — [docs/notes/history.md](docs/notes/history.md) "Architecture Notes"/ninth round. (Note: a hash-chained audit log was later added anyway, `b9ddc105`.)
- **Sleep consolidation pass** produced zero memory entries in 60 sessions on flash; extraction did all capture — recorded negative — [docs/benchmarks/memory/REPORT.md](docs/benchmarks/memory/REPORT.md).
- **Spec review with check scripts / 6-step cap**: cost ~150 s/run (5,100 s over 32 runs), more than the whole gap to pi; replaced by read-only review — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).
- **Feature ablations on multistep** (todo, coach+syntax, repomap, verify off): no pass-rate effect resolvable at 3 runs; only resource effects resolve — [docs/benchmarks/multistep/REPORT.md](docs/benchmarks/multistep/REPORT.md); [docs/notes/history.md](docs/notes/history.md).
- `then_run` went unused by MiMo (already batches edit+check) — kept, unmeasured — [docs/benchmarks/lean-ab/REPORT.md](docs/benchmarks/lean-ab/REPORT.md).

### Inferences
- A recurring pattern: ideas that add a model call or a schema field per step lose on cost in janetcode's short-session regime; ideas that remove a round trip deterministically win.
- The "harder eval" problem recurs everywhere: hard/gated/multistep sets saturated on capable models, so harness features rarely resolve on pass rate. effbench is the first set where pass-rate differences appeared, and only at n=32.

### Gaps
- No experiment yet on a model that genuinely does one-step-at-a-time (the case `run_plan` targeted).
- Advisor `gated` mode never built; strong-slot candidate `z-ai/glm-5.3` untested.
- Memory probe driver is not committed, so memory claims can't be reproduced from the repo.

## Q3. Where runs fail: dominant failure modes, bottlenecks, known gaps

### Takeaway
Current residual failures are (a) hidden-test rules the public tests don't reach, (b) a hard task (d-minire) nobody reliably solves, (c) reasoning-model over-planning/burst pathologies on first reply, (d) test-tampering, and (e) infrastructure (broker 504s, upstream mixing). Cost is dominated by model wall time; compaction and long-horizon behaviour are essentially untested.

### Cited Findings
- r13 janetcode misses: a-build-incremental s2, d-minire s2; d-minire is passed only rarely by anyone (janetcode passed it a few times total; pi never on MiMo here) — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md).
- Recurring misses pre-spec-review: "one hidden test each" after public tests passed (a-catalog-paging validated min/max_price but never filtered; c-go-cache let a concurrent Set exceed capacity) — [internal/agent/specreview.go](internal/agent/specreview.go); effbench REPORT.
- Reasoning blowups: first reply over an inlined repo reasoned 23–33k tokens and nothing written; three held-out runs ended at the 20-minute cap without an edit; pi's median reply reasons ~100 tokens — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md) r5.
- Bursts: replies starting 37–392 tool calls, one d-minire run spent 12 minutes; d-cronexpr: 5 replies × 24 calls with 3–9 distinct, hit 120-call turn ceiling — same source.
- Output-limit truncation: r3 had 9 cut replies, 1,971 s and 144k output tokens thrown away (fixed by 32k default + doubling) — same source.
- Tamper: model rewrote test_public.py; wrote/loosened its own test file to match code — same source.
- Spec review now makes janetcode's total input 13% higher than pi's (rereading cached context) — same source r13.
- Run-to-run variance: per-task wall time varies 2–3× between seeds; session set 21% spread between identical runs (113% on search-heavy task); single task varies 2× calls, 3× wall across its own runs — [docs/benchmarks/effbench/REPORT.md](docs/benchmarks/effbench/REPORT.md); [README.md](README.md); [docs/notes/jev.md](docs/notes/jev.md).
- Latency: ~98% of wall time is the model (21–28 tok/s), tools ~1%; prompt cache ~90%; context stays ~35k — [docs/notes/history.md](docs/notes/history.md) "Dogfood round 1". Per-turn latency tracks output tokens; 79% of output is tool arguments, edit_file the largest consumer, 54% of its bytes `old_string` (sonnet) — [docs/benchmarks/lineedit/REPORT.md](docs/benchmarks/lineedit/REPORT.md).
- Exploration share: on a 58-file repo exploration is 43–50% of model calls and 40–54% of the run before the first edit; 54–55% of tool results never referenced again; 7.3–7.7% of input stale; tool output 36% of input — [docs/notes/history.md](docs/notes/history.md) "Context curator round".
- Compaction: across every benchmark run the compaction count is zero; under forced 6k pressure pass rate 18/18 → 7/18 with 11 runs stuck re-reading files; post-compaction recall 9/20 with `expand_output`, 0/16 blind; retrieval (query phrasing) is the whole loss — [docs/benchmarks/compaction/PRESSURE.md](docs/benchmarks/compaction/PRESSURE.md); [docs/benchmarks/compaction/FACTS.md](docs/benchmarks/compaction/FACTS.md).
- Memory capture bottleneck: lesson captured in only 4/9 trap-sprung sessions — [docs/benchmarks/memory/REPORT.md](docs/benchmarks/memory/REPORT.md).
- Gate realism: capable models run the tests themselves, so gates almost never fail (236 gate commands, 0 failures; 16, 0) — "a truthful gate confirms, it does not catch" — [docs/notes/history.md](docs/notes/history.md).
- Containment: eval runs are not sandboxed; two runs "fixed the environment instead of the code" (compiled into ~/bin, bound a port) — [docs/notes/jev.md](docs/notes/jev.md).
- Infra: ~45% of broker runs drop as INFRA (504 over mycelium); broker silently served Kimi for every model name on 2026-10-01; upstream mix (GMICloud writes ~3× less than Novita) confounds comparisons — [PROJECT-STATE.md](PROJECT-STATE.md); [docs/benchmarks/lean-ab/REPORT.md](docs/benchmarks/lean-ab/REPORT.md).
- Large repos: git fingerprint for shell cache 0.67 s cold on kubernetes before every bash call; glob caps 1,000, grep 500 — [docs/notes/large-repos.md](docs/notes/large-repos.md).
- Known gaps list: Wilson CI pools runs as iid; seatbelt untested on Mac; Landlock egress by port; docker egress reaches host 0.0.0.0 services; Anthropic path not verified against api.anthropic.com; native Gemini only against fake server; app.js ~2,000 lines; durable turns lose the undo manifest on crash and partial streamed text — [PROJECT-STATE.md](PROJECT-STATE.md); [docs/notes/durable-turns.md](docs/notes/durable-turns.md).
- Remaining unfixed security notes from reviews: fetch_url DNS-SSRF and remote-MCP private-target guards literal-IP-only; fallback.go classifies errors by free-text regex — [docs/notes/history.md](docs/notes/history.md) "Aggressive-review round" (may have since changed; not re-verified).

### Inferences
- The best-supported targets for new ideas: (1) catching unstated/edge-rule requirements cheaply (spec review works but costs input); (2) taming reasoning-model first-reply over-planning beyond a hard char budget; (3) long-horizon/large-repo context behaviour, which no eval exercises; (4) post-compaction retrieval quality; (5) memory capture rate.
- Pass-rate claims on effbench rest on 32 runs per arm with 2–3× per-seed variance; differences of 2–4 passes are near noise.

### Gaps
- No per-failure taxonomy across all r8–r13 runs was found as a single table; misses are listed per campaign in prose.
- No measurement of interactive-TUI user sessions' failure modes beyond dogfood notes.

## Q4. What is already unusual or distinctive

### Takeaway
Distinctives: every mutation shown as a diff before it happens; a harness-computed verdict of what a turn demonstrated vs claimed; per-hunk provenance with stated intent attached to git commits as proof notes; serving-model provenance guard; counterfactual replay and recipes from recorded sessions; mutation-testing gate; pre-edit impact analysis; NEED-protocol retrieval after compaction; and a fail-open structured-decision (Jev) integration with replay corpora.

### Cited Findings
- "every mutating action shown to you as a diff before it happens" — [README.md](README.md); pre-run diff for pending edits, sed -i shown as a diff — [docs/notes/history.md](docs/notes/history.md).
- Verdict vs claim; "What turns demonstrate" replay over 97 real turns: 42% verified, false accusations 13 → 0 — [README.md](README.md); `internal/agent/verdict.go`.
- Proof git notes — `internal/proof`; hunk provenance + intent — `internal/change`, `agent/provenance.go`.
- Provenance guard for gateway model substitution — `internal/provenance`.
- `internal/whatif` counterfactual replay with zero tokens; `internal/replay` sessions as cassettes; `internal/recipe` crystallised tool programs — package docs.
- `internal/mutate` mutation gate; `internal/gaming` test-gaming detector; `/accept` approved acceptance tests the model can't weaken — package docs.
- `internal/impact` pre-approval blast radius — package doc.
- NEED protocol — `internal/agent/need.go`.
- Hidden held-out acceptance gate with leak-proofing tests — [docs/notes/history.md](docs/notes/history.md).
- Typed working-memory handoff instead of prose summary; mechanical "Already read" lists — `agent/working.go`; PRESSURE.md.
- Durable turns with mutating-call evidence from disk — [docs/notes/durable-turns.md](docs/notes/durable-turns.md).
- Small fixed overhead: prompt+schemas ~1.8k tokens vs 18k/23k/70k for other harnesses; headless base prompt ~1 KB where every line was put there by a measurement — [README.md](README.md); `internal/prompt/headless.go`.
- Self-describing feature catalogue enforced by tests — [docs/notes/history.md](docs/notes/history.md).

### Inferences
- A "novel idea" touching verification evidence, provenance, replay, routing, decision-model gating, memory typing, or compaction handoff is likely to overlap something already here; proposals should name the existing package and say what is different.

### Gaps
- Several distinctive features (proof notes, recipes, whatif, accept, impact, mutate) have no benchmark report showing measured effect on pass rate or cost.

## Q5. Recent commit themes and direction

### Takeaway
The last ~150 commits move from breadth (parity features, safety, UI, providers) to a tight measure-and-fix loop against pi on hero_agent's effbench with a cheap reasoning model (MiMo), focused on headless efficiency and correctness guards.

### Cited Findings
- Most recent commits (top of `git log`): spec review reads code (`5c3ed1a2`), review wraps after N steps (`e4dfa3a4`), test-file first-write refused (`1b033e85`), green headless turn checked against task (`f6cd0fae`), repeat-call cut (`8d768119`), burst cap (`6e50c06f`), plan budget (`daf2d337`, `1d4d3e56`), headless short base prompt (`dedc0555`), default output 32k (`b634a73e`), headless coding core tools (`f1226498`), cut-off reply raises limit (`5c9d164d`) — `git log --oneline -150`.
- Before that: lean execution (inline small repo `bf217f01`, then_run `0d840368`, async LSP `3e42c811`, cache-stable deferred tools `2131f721`, identical read-only calls deduped `e85a7218`, usage metering cross-check `04dc725d`) — same.
- Earlier in the window: qwengo parity (run limits, tool filters, .aiignore, safe mode, search backends, bwrap, /commit, /pr, Gemini/Responses providers, json-schema, stream-json input), borrowed-and-measured round (provenance guard, durable turns, verify_baseline, timed questions, MCP OAuth hardening, recovery-oriented error messages), user-expectation gaps (Windows fixes, typed perms, plugins, egress proxy, audit log, VS Code/JetBrains) — same; [CHANGELOG.md](CHANGELOG.md).
- Next steps listed: live-verify api.anthropic.com, cut v0.3.0 (~1,040 commits since v0.2.0) — [PROJECT-STATE.md](PROJECT-STATE.md) "Next".

### Inferences
- Direction: optimise the headless agent for cheap reasoning models (fewer round trips, guarded reasoning budgets, anti-tamper, requirement re-check) and prove it against pi on a third-party benchmark; broader "intelligent" layers (Jev, routing, critic) are parked pending harder corpora.
- Risk of overfitting to effbench/MiMo is acknowledged in the report caveats (one model, tuned on these tasks, 2 seeds).

### Gaps
- No stated roadmap beyond release and live Anthropic verification; the research branch name (`research/frontier-ideas`) suggests a new ideation round, but no committed plan was found.
