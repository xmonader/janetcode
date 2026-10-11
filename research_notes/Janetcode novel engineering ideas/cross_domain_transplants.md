# Cross-Domain Engineering Transplants for a Go Terminal Coding Agent (janetcode)

Scope note: about 17 tool calls (searches plus 2 abstract fetches). Most 2026 arXiv items come from search snippets, aggregator summaries or abstract pages, not full-text reads. Their numbers are author-reported unless marked otherwise. References to the classic source-domain work (Salsa, rr, ddmin, Tarantula/Ochiai, CEGIS, egg, Pijul) are canonical citations; I did not re-fetch them this session, and they are flagged "(canonical, not fetched)". Each section below is one transplant candidate in the requested format: source technique, mapping onto the janetcode loop, why it could be a step change, and prior art. The sections are ordered roughly by value/effort for a single Go binary.

---

## Q1. Speculative execution / branch prediction: can tool calls run ahead of the model?

### Takeaway
This is an active 2026 research line with strong reported latency wins (PASTE: 43.5% lower end-to-end task time). As prior art it is no longer novel. A cheap, safe janetcode version is still open: speculate only on read-only, sandboxed tools (read/grep/LSP/`go test -run` in a scratch overlay) and discard on mispredict.

### Cited Findings
- PASTE ("Act While Thinking", arXiv 2603.18897, SJTU/Microsoft Research) predicts future tool calls from recurring tool-call sequences and parameter-passing patterns. It runs them while the LLM is still generating and "isolates speculative results until confirmed by the LLM". It reports 43.5% lower average task completion time and 1.8x lower observed tool latency across deep-research, coding and scientific-agent workloads. The abstract reports no hit rates and does not say how side-effecting tools are handled. — [arXiv abs 2603.18897](https://arxiv.org/abs/2603.18897); [MSR page](https://www.microsoft.com/en-us/research/publication/act-while-thinking-accelerating-llm-agents-via-pattern-aware-speculative-tool-execution/)
- Nichols et al., "Optimizing Agentic LM Inference via Speculative Tool Calls" (arXiv 2512.15834) speculates on tool calls and proposes a provider-side "tool cache" endpoint (search-summary level). — [arXiv 2512.15834](https://www.arxiv.org/pdf/2512.15834)
- SPORK (arXiv 2607.03333) is a training-free method in which the model predicts its own next tool name from a forked probe. Reported: about 18% lower P95 latency on GAIA with Qwen3-32B (aggregator summary). — [alphaxiv 2607.03333](https://www.alphaxiv.org/abs/2607.03333)
- "Speculate While You Reason" (arXiv 2607.25816) trains the agent and a speculator jointly with RL to predict the next tool call. — [alphaxiv 2607.25816](https://www.alphaxiv.org/abs/2607.25816v1)
- Speculative Macro Commit (arXiv 2609.03236) uses a fast drafter that runs predicted action chains on an environment snapshot, checks them against a macro library mined from traces, and lets a large actor commit. — [arXiv 2609.03236](https://arxiv.org/pdf/2609.03236)
- A secondary summary mentions a "Ghost Tool Calls" paper claiming that speculative dispatch leaks intent to observers. I could not verify it. — [keenable listing](https://select.keenable.ai/r/similar-papers-hidden-state-forecasting-and-speculative-tool-use/R7Ll2tDorW27tiWWxEZlIA)

### Inferences
- Mapping for janetcode: (a) A Markov/n-gram predictor over janetcode's own session logs (tool name plus argument-derivation pattern, e.g. "after `grep` hit in file F, next is `read F`"). (b) While the model streams, fire predicted read-only calls into a result buffer keyed by (tool, canonical args, content hash of inputs). (c) When the real call arrives and matches, serve it from the buffer. Go goroutines plus context cancellation make this about 200 lines. Restricting to idempotent tools removes the side-effect problem that PASTE leaves unspecified.
- A coding-specific extension: after every `edit`, speculatively start the build/typecheck (`go vet`, LSP diagnostics) and the affected-test run in the sandbox before the model asks. The model nearly always verifies after editing, so this is close to a guaranteed hit. Combined with Q2 it hides most verification latency.
- Streaming tool-call parsing is a further level: start a `read` once the path argument has streamed, before the JSON closes.
- Real novelty is limited (the speedup idea is taken), but speculative verification-after-edit in particular appears not to have been written up.

### Gaps
- No public hit-rate data for coding-agent tool prediction. janetcode's session logs could measure it directly.
- I could not confirm the "Ghost Tool Calls" privacy claim.

---

## Q2. Incremental / self-adjusting computation and build graphs: can verification be incremental and tool results cached?

### Takeaway
There is solid evidence that targeted, dependency-based test selection beats both "run everything" and "LLM picks tests": TDAD cut regressions 70%, and testmon-style selection runs 47–99% fewer tests. Content-addressed tool-result caching exists in small libraries but not as a principled Salsa-style query graph inside an agent. That graph is the novel transplant.

### Cited Findings
- TDAD (arXiv 2603.17973) builds a source-to-test dependency map (static text file the agent queries) and runs selection before the agent submits. On SWE-bench Verified with open-weight models, regressions fell from 6.08% to 1.82% (−70%). Adding TDD instructions without targeted test context made regressions worse (9.94%). The baseline agent broke an average of 6.5 previously passing tests per patch. — [arXiv 2603.17973v2](https://arxiv.org/html/2603.17973v2)
- An agentic issue-resolution survey notes that current scaffolds mostly choose tests by LLM judgment rather than dependency analysis (search-summary level). — [arXiv 2512.22256](https://arxiv.org/pdf/2512.22256)
- PerfAgent (arXiv 2607.19653) uses pytest-testmon coverage-based selection and runs 47–99% fewer tests than the full suite. — [arXiv 2607.19653](https://arxiv.org/pdf/2607.19653)
- kedge-cache keys tree-sitter compaction results on sha256 of file bytes ("a perfect key"), which makes staleness automatic. It deliberately does not cache LLM output. — [docs.rs kedge-cache](https://docs.rs/crate/kedge-cache/0.1.0)
- tool-result-cache (Rust) is an LRU keyed on a hash of (tool_name, args) with TTL. — [docs.rs tool-result-cache](https://docs.rs/tool-result-cache)
- The async-cache "Agent Cache" fires invalidation before a mutation executes, so stale data is never served. — [async-cache docs](https://async-cache.readthedocs.io/en/latest/agent_cache.html)
- An MCP caching guide warns that stale cached data is worse than no cache because the agent acts on it unknowingly. — [fast.io guide](https://www.fast.io/resources/mcp-server-caching.md)
- Source domain (canonical, not fetched): Salsa, the incremental query engine behind rust-analyzer (red-green revalidation, durability levels); Adapton (Hammer et al., PLDI 2014); Bazel/Buck2 content-addressed action cache. — [salsa-rs](https://github.com/salsa-rs/salsa); [Buck2](https://buck2.build/)

### Inferences
- Mapping: model each tool as a pure query over content-addressed inputs. `read(path)` depends on blob hash, `grep(pattern, scope)` on the tree hash of the scope, `go test ./pkg -run X` on the hashes of the transitive package closure (from `go list -deps`) plus toolchain version. Store results in a small on-disk CAS (BoltDB/SQLite, or plain files under `.janetcode/cache/<sha>`). Results:
  1. Re-reads after rewind/fork become free and are provably fresh. This fits janetcode's existing fork/rewind, since sessions branch over the same content.
  2. "Early cutoff" (the Salsa/Buck2 idea): if an edit changes a file but `gofmt`ed AST/export data is unchanged, downstream test results stay valid. That skips re-verification after comment/doc-only edits.
  3. Minimal test selection for Go is unusually cheap. `go list -deps -test` gives package-level dependency closure, and Go's own test cache already content-addresses test results. janetcode can expose "affected tests" as a first-class verification-gate input instead of having the model guess.
- Step-change argument: TDAD shows the gain is not only speed. Giving the agent the right tests changes correctness (−70% regressions), while procedural prompting alone hurts.
- Novelty: TDAD and testmon are prior art for selection. A unified content-addressed query graph across all agent tools, with early cutoff and fork-aware sharing, was not found in prior art.

### Gaps
- No measured cache-hit rates for agent tool calls in real sessions were found.
- I did not find a paper on Salsa-style early cutoff applied to agent verification.

---

## Q3. Databases (MVCC, WAL, transactions, sagas, OCC): transactional edits and parallel agents?

### Takeaway
Concurrency control for multi-agent LLM systems emerged as a research topic in 2026. CoAgent's MTPO protocol (speculative writes plus saga inverses plus "runtime informs, agent repairs") reports a 1.4x speedup within 5% of serial correctness, with OCC and 2PL losing nearly all gains. For janetcode the cheapest high-value transplant is a WAL-backed atomic multi-file edit transaction, plus file-version OCC for parallel sub-agents.

### Cited Findings
- CoAgent (arXiv 2606.15376) addresses problems specific to agents: transactions span minutes of inference, read sets are broad and opaque, and writes take effect immediately. Locking blocks during inference; OCC discards significant work. MTPO fixes a serialization order at launch, filters reads by that order, applies writes speculatively in place, notifies affected readers to re-judge and patch their plans, and undoes or reorders misplaced writes with saga-style inverses that each tool registers. Results: within 5% of serial correctness, 1.4x speedup, near-serial token cost on ten contended workloads. ToolSmith grew a 25-tool library online and raised pass rate from 45/71 to 63/71 at 0.80x time and 0.86x cost. — [arXiv abs 2606.15376](https://arxiv.org/abs/2606.15376)
- A secondary summary reports OCC at 0.93x of serial on the same workloads (not verified against the paper). — [codex.danielvaughan.com](https://codex.danielvaughan.com/2026/07/11/coagent-concurrency-control-multi-agent-coding-codex-cli-mtpo-parallel-sessions-conflict-resolution/)
- "Multi-agent Collaboration with State Management" (arXiv 2605.20563) uses a per-file version counter. Reads return the version, writes must declare the expected version, and mismatches are rejected, on the premise that well-decomposed tasks mostly touch disjoint files. — [arXiv 2605.20563](https://arxiv.org/pdf/2605.20563)
- Clash (product listing) watches git worktrees and warns about overlapping edits while agents work, integrated through Claude Code hooks. — [Product Hunt](https://www.producthunt.com/posts/1229056)

### Inferences
- Mapping for janetcode, three layers:
  1. **Edit transactions with a WAL.** A model turn that edits N files becomes `BEGIN; edit…; VERIFY; COMMIT|ROLLBACK`. Before applying, write intent records (path, pre-image hash, post-image) to an append-only log under the session dir and fsync. Then apply. Crash or verification-gate failure means replay the inverse. This gives atomic multi-file diffs: the user sees and approves one diff set, and a half-applied refactor cannot persist. It also unifies rewind (rewind = replay WAL inverses). Pure Go, no infra.
  2. **Read-set tracking plus OCC for sub-agents/forks.** Record (path, blob hash) for every `read`. On merge-back of a forked sub-agent, validate its read set against current hashes, as in Silo/Hekaton-style validation. Do not abort on conflict; adopt CoAgent's lesson and hand the conflicting delta to the agent to re-judge ("runtime informs, agent repairs"), because aborts waste inference.
  3. **Saga inverses for bash.** Require shell commands that the agent tags as mutating to declare a compensating action, or run them inside the sandbox overlay so the "inverse" is discarding the overlay.
- Step change: parallel sub-agents become safe by construction rather than by prompt instructions. The WAL makes "show diff before mutation" transactional across files.

### Gaps
- No independent replication of CoAgent's numbers was found.
- Read-set precision is unclear: agents "read" through grep/bash output too. A conservative approach treats any grep over a scope as a range read (predicate locking analogy). This is untested.

---

## Q4. Time-travel / record-replay debugging and dynamic tracing: can agents get runtime truth?

### Takeaway
2026 papers show agents benefit from real execution evidence: DebugHarness with rr, TraceCoder probe instrumentation, TraceRepair (392 Defects4J fixes) using runtime snapshots to reject overfitted patches. eBPF-based observation of agents exists (AgentSight) but is used for auditing, not as a feedback tool for the model. For a Go agent the cheap transplant is "trace-on-failure": rerun a failing test under Delve/`GOTRACEBACK`/coverage and hand the model the dynamic facts.

### Cited Findings
- DebugHarness (arXiv 2604.03610) pairs an LLM with real debuggers and integrates Mozilla rr record-and-replay so the agent can step backward to the origin of memory corruption, motivated by GDB's inability to undo a step. — [arXiv 2604.03610](https://arxiv.org/pdf/2604.03610)
- TraceCoder (arXiv 2602.06875) instruments code with probes to capture fine-grained runtime traces, does causal analysis, keeps a historical lesson store, and rolls back to the last correct state. — [arXiv 2602.06875](https://arxiv.org/html/2602.06875)
- TraceRepair (arXiv 2604.02647) uses a probe agent to snapshot critical variables. A committee validates candidate patches against the snapshots to filter overfitted or hallucinated fixes. Reported: 392 Defects4J defects correctly fixed. — [arXiv 2604.02647](https://arxiv.org/html/2604.02647v1)
- AgentSight (arXiv 2508.02736) intercepts TLS LLM traffic for semantic intent, monitors kernel events through eBPF, and causally correlates the two streams. It requires Linux with CAP_BPF/CAP_SYS_ADMIN. — [arXiv 2508.02736](https://arxiv.org/abs/2508.02736v1); [docs.rs agentsight](https://docs.rs/crate/agentsight/0.2.25)
- Commentary argues sandbox/approval policies are preventive while OTel output is self-reported, so an independent kernel audit layer is useful. — [danielvaughan eBPF post](https://codex.danielvaughan.com/2026/06/09/ebpf-runtime-observability-codex-cli-agentsight-tetragon-kernel-level-agent-monitoring/)
- AgentDebugX (arXiv 2607.18754) argues that replaying a trace alone rarely identifies the failure-causing decision and proposes Detect/Attribute/Recover/Rerun. — [alphaxiv 2607.18754](https://www.alphaxiv.org/abs/2607.18754)

### Inferences
- Mapping for janetcode:
  - A `trace` tool: given a failing test, rerun it with Go coverage (`-coverprofile`) and, optionally, Delve in headless mode with automatic breakpoints on the failing assertion's stack frames. Return a compact "dynamic facts" block (executed lines in touched files, the locals at the failure frame). rr works for Go binaries on Linux x86, giving reverse-continue to "where did this value get set".
  - Use the existing bwrap sandbox plus `strace -f -e trace=file,network` (no root needed, unlike eBPF) so every bash tool result carries a ground-truth summary of the side effects: "files written: …, network: …". That gives the model runtime truth about what a command did, and gives provenance notes a verifiable basis. This is cheap and probably not done by any terminal agent.
  - Snapshot-validation à la TraceRepair: record variable snapshots on the passing version, then require the patched version to preserve them outside the fault region. This is an anti-overfitting gate.
- Novelty: agent-side side-effect summaries from the sandbox, fed back to the model as feedback rather than used only for audit, were not found as prior art.

### Gaps
- No data on token cost vs benefit of feeding traces to the model.
- rr does not run on macOS/ARM in general, so a seatbelt/macOS path needs a different backend (dtrace is restricted under SIP). This is a portability gap.

---

## Q5. Delta debugging, mutation testing, fuzzing, slicing, fault localization (SBFL) as agent tools

### Takeaway
SBFL is proven prior art in an LLM agent: AutoCodeRover reports fault localization lifting single-run efficacy from 16% to 20% on SWE-bench-lite. Dynamic slicing for LLM repair is just emerging (CausalRepair, 2026). LLMs cannot do slicing themselves reliably (GPT-4o about 60%), which argues for deterministic tools. Delta debugging as an agent tool, minimizing failing inputs or diffs, is largely unclaimed.

### Cited Findings
- AutoCodeRover (arXiv 2404.05427, ISSTA 2024) optionally uses SBFL over passing/failing tests to rank suspicious methods and prioritizes methods that appear both in the issue and in the SBFL output. v1 reports efficacy rising from 16% to 20% in a single run with FL. v3 reports 19% on SWE-bench-lite at $0.43/issue. The versions differ in how the numbers are presented. — [arXiv 2404.05427v1](https://arxiv.org/html/2404.05427v1); [v3 abs](https://arxiv.org/abs/2404.05427v3)
- "Program Slicing in the Era of LLMs" (arXiv 2409.12369): GPT-4o reached about 60% accuracy on Java static and dynamic slicing, so LLMs are "yet to achieve reasonable performance" at slicing. — [arXiv 2409.12369](https://arxiv.org/html/2409.12369v1)
- CausalRepair (arXiv 2608.10613) argues dynamic slicing gives a high signal-to-noise context for LLM repair, combining static slicing of tests with dynamic slicing of source. Snippets cite LLMxCPG CPG slicing cutting code volume >90% for vulnerability detection. — [arXiv 2608.10613](https://arxiv.org/pdf/2608.10613)
- Delta Debugging for LLM-integrated systems (AWS, ICSE 2026 SEIP) applies ddmin with "semantic markers" to isolate the prompt fragments behind bad outputs. That is ddmin on the agent's inputs, not as an agent tool. — [researchr ICSE 2026](https://conf.researchr.org/details/icse-2026/icse-2026-software-engineering-in-practice/5/Delta-Debugging-for-LLM-integrated-Systems); [Amazon Science PDF](https://cdn.amazon.science/26/30/8b1ecc7a466f8ec39bebfa72d36b/scipub-approval152129-41541789-delta-debugging-for-llmintegrated-systems.pdf)
- LLM-driven mutation testing with iterative "scientific debugging" (arXiv 2503.08182): the generated test runs on both the original and the mutant and is refined if it does not distinguish them. Evaluated with gpt-4o-mini against Pynguin. — [arXiv 2503.08182](https://arxiv.org/pdf/2503.08182)
- Mokav (ASE 2025) uses an LLM to generate difference-exposing tests between two program versions. — [researchr ASE 2025](https://conf.researchr.org/details/ase-2025/ase-2025-journal-first-track/39/Mokav-Execution-driven-Differential-Testing-with-LLMs)
- Source domain (canonical, not fetched): Zeller & Hildebrandt ddmin (IEEE TSE 2002); Tarantula (Jones & Harrold 2005), Ochiai SBFL.

### Inferences
- Mapping for janetcode, each a deterministic Go tool:
  1. **`localize`**: run the test suite with per-test coverage (`go test -coverprofile` per test or `-json` plus cover), compute Ochiai per line/function, and return the top-k. That is about 150 lines of Go, and AutoCodeRover is evidence it helps.
  2. **`minimize`** (ddmin over the agent's own diff): when a multi-hunk edit breaks a test, run ddmin over hunks to find the minimal failing subset and report "hunk 3 of 7 alone causes the failure". This turns the agent's often-large patches into causal evidence. It is the delta-debugging idea applied to the agent's own patch history, and I found no prior art for that. It also works on failing inputs (fuzz crash minimization; Go's native fuzzer already minimizes).
  3. **`mutate`-gate** (mutation testing as test-adequacy check): after the agent writes a test, apply a few AST mutations to the code under test (flip conditional, off-by-one) and check the new test kills them. An undetected mutant means the test is vacuous. This targets a known agent failure mode, tests that pass trivially, and fits janetcode's verification gates and its existing refusal of headless first writes to tests.
  4. **Differential test (Mokav-style)**: for refactors, auto-generate inputs and compare old vs new binary output inside the sandbox.
- Step change: these move the agent from "read code and guess" to "query causal facts", and the evidence shows LLMs cannot compute these facts themselves (slicing at 60%).

### Gaps
- No published measurement of ddmin-over-agent-hunks or mutation-gated agent tests was found. These are novel and untested.
- The AutoCodeRover FL gain numbers differ across paper versions.

---

## Q6. Semantic code search via Datalog/CodeQL, and code graph databases

### Takeaway
LLM plus CodeQL exists for vulnerability-query synthesis (QLCoder) and repository Q&A (Merlin, unverified), and CodexGraph shows graph-query navigation for agents. A lightweight embedded Datalog over Go facts (from `go/packages` and `go/ssa`) inside the binary would avoid CodeQL's heavy infra and licensing. I found no prior art for that specific form.

### Cited Findings
- QLCoder (arXiv 2511.08462, ICLR 2026) is an agent that synthesizes CodeQL queries from CVE metadata, using a CodeQL language server exposed over MCP. — [arXiv 2511.08462](https://arxiv.org/pdf/2511.08462)
- CodexGraph (NAACL 2025) lets an LLM agent write graph-database queries over code nodes (modules, classes, functions) for structure-aware retrieval. — [arXiv 2408.03910](https://arxiv.org/html/2408.03910v1)
- An Emergent Mind topic page describes "Merlin", pairing LLMs with CodeQL for free-form developer questions and arguing grep is inadequate at scale. I did not find the original paper. — [emergentmind CodeQL](https://www.emergentmind.com/topics/codeql)

### Inferences
- Mapping: extract facts in Go (`calls(f,g)`, `defines(pkg,sym)`, `implements(T,I)`, `writes(f,field)`, `test_covers(t,f)` from Q5 coverage). Evaluate with a small embedded Datalog engine (semi-naive evaluation is about 500 lines) and expose `query` as a tool. Example: "all callers transitively reaching `os.Exec` from handlers". This complements LSP, which answers single-hop questions, with recursive ones.
- With Q2, incremental Datalog (DRed / differential dataflow style) keeps facts fresh after edits.
- Risk: models may write bad Datalog. CodexGraph's experience suggests models can write graph queries, but the evidence here is thin.

### Gaps
- No benchmark comparing Datalog/graph-query tools against grep plus LSP for coding agents was found.
- Merlin is unverified.

---

## Q7. Program synthesis (CEGIS) and e-graphs

### Takeaway
LLM-as-generator CEGIS loops are established (LLM proposes, verifier returns a concrete counterexample). janetcode's verification gates are already a degenerate CEGIS. The transplantable sharpening is to always return a minimal counterexample (via ddmin / Go fuzz minimization) and accumulate counterexamples as a growing spec. For e-graphs I found no coding-agent prior art this session.

### Cited Findings
- Standard LLM-CEGIS: an LLM generates a candidate, a verifier (SMT, static analyzer, compiler, tests) checks it, and a failing check yields a concrete counterexample folded into the next prompt. — [curate-ipsum synthesis design](https://curate-ipsum.readthedocs.io/en/latest/design/synthesis_framework.html)
- Jha et al. place the LLM as a "scalable learner" inside CEGIS with an SMT solver as verifier for planning/synthesis. — [arXiv 2309.16436](https://arxiv.org/pdf/2309.16436)
- ConVer (arXiv 2605.27051) runs a CEGAR contract-refinement loop for up to 5 iterations before escalating to CEGIS. — [arXiv 2605.27051](https://arxiv.org/pdf/2605.27051)
- Source domain (canonical, not fetched): CEGIS (Solar-Lezama et al., ASPLOS 2006 sketching); egg e-graphs (Willsey et al., POPL 2021). — [egg arXiv 2004.03082](https://arxiv.org/abs/2004.03082)

### Inferences
- Mapping: keep a persistent, per-task counterexample set (failing inputs, minimized), and replay all of them against each new candidate before the model sees a result. This prevents fix-A-break-B oscillation, the classic CEGIS guarantee that past counterexamples stay excluded.
- E-graph idea (speculative, unsupported by prior art found): use equality saturation to canonicalize candidate patches so that semantically identical candidates from parallel samples are deduplicated before costly verification. This is probably too heavy for its value in Go; low priority.

### Gaps
- No search evidence on e-graphs applied to coding agents. This is treated as unexplored, not as proven absent.

---

## Q8. Control theory, schedulers and economics: how much compute per step?

### Takeaway
Adaptive per-step compute is now studied. TrACE uses inter-rollout agreement (reported 33–65% compute cut at similar accuracy, secondary), and LIMBO uses a contextual bandit (LinUCB) per task. I found no work on per-step bandit allocation specifically for coding agents. janetcode could use cheap signals it already has (verification-gate outcomes, diff size, test deltas) as the controller's feedback.

### Cited Findings
- TrACE (arXiv 2604.08369) is training-free. It samples a few candidate actions per step; agreement means commit, disagreement means draw more rollouts up to a cap. A secondary summary reports 33–65% lower compute than fixed budgets on GSM8K/MiniHouse at similar accuracy. — [arXiv 2604.08369](https://arxiv.org/pdf/2604.08369)
- LIMBO (arXiv 2609.14138) is a gradient-free LinUCB extension that picks a budget configuration (token budget, rounds, tool-call limits, memory replay) per task; experiments are on DBBench. — [arXiv 2609.14138](https://arxiv.org/pdf/2609.14138)
- TAB (arXiv 2604.05164) learns per-turn token budgets as a multi-objective MDP and reports up to 35% token savings on math. — [emergentmind 2604.05164](https://www.emergentmind.com/papers/2604.05164)
- AVA (TMLR 2026) splits budget across search, sampling and verification using value-of-information and includes HumanEval. — [mlanthology AVA](https://mlanthology.org/tmlr/2026/patel2026tmlr-anytime/)
- CostAda (arXiv 2607.26828) ranks code-evolution frontiers by progress per unit of realized cost (bandits-with-knapsacks). — [alphaxiv 2607.26828](https://www.alphaxiv.org/abs/2607.26828.md)

### Inferences
- Mapping options for janetcode:
  - **PID/AIMD controller on effort.** Error signal = (failing tests count, gate failures, repeated identical tool calls). Raise reasoning effort or switch to the stronger model on rising error; decay multiplicatively when progress is steady (TCP AIMD analogy). This is trivial in Go and works with any broker/model.
  - **Agreement gate (TrACE) only at high-stakes steps**, such as before a mutating edit or a final "done", where sampling 2–3 drafts and checking diff agreement is cheap relative to a wrong edit.
  - **Scheduler analogy (MLFQ):** sub-agents that keep making progress keep their quantum; those that loop get demoted.
- Novelty: per-step bandit/feedback control driven by coding-specific signals was not found.

### Gaps
- Most numbers come from math/QA benchmarks; their transfer to coding is unknown.

---

## Q9. Version control research: structural diff/merge and patch theory

### Takeaway
Mergiraf (tree-sitter structured merge, falling back to line merge on parse failure) is a drop-in for merging parallel agent branches and for showing structural diffs. Pijul's commutation algebra gives a principled model for "do two agents' patches commute?" No sources link either to LLM agents (one skill page is speculative), so this is open territory.

### Cited Findings
- Mergiraf parses base/left/right with tree-sitter and merges syntax trees in three phases (auto-tuning, structured matching, amalgamation). It is a generic re-implementation of Spork over tree-sitter CSTs. If any revision fails to parse, it falls back to line-based merging. — [mergiraf architecture docs](https://mergiraf-docs.statichost.eu/architecture.html); [emergentmind mergiraf](https://www.emergentmind.com/topics/mergiraf)
- MergirafSemi (arXiv 2608.11345) is a language-agnostic semistructured merge tool. — [arXiv 2608.11345](https://arxiv.org/pdf/2608.11345)
- Pijul: any two changes either commute (applicable in any order with the same result) or have an explicit dependency. The same conflict appears regardless of application order, and deletions depend on surrounding context lines so "awareness" of other patches is recorded. — [Pijul commutation post](https://pijul.org/posts/2020-12-19-partials); [Pijul discourse](https://discourse.pijul.org/t/why-these-patches-dont-commute/449)
- The only agent link found is a third-party skill page (speculative). — [skills.sh pijul](https://www.skills.sh/plurigrid/asi/pijul)

### Inferences
- Mapping: (1) Invoke mergiraf (an external binary) or reimplement a Go tree-sitter-based structural three-way merge when merging fork/sub-agent branches. Only unresolved structural conflicts go to the LLM, which cuts tokens and spurious conflicts. (2) Show structural diffs (difftastic-style) in the pre-mutation diff UI to reduce reviewer burden. (3) Pijul-style commutation check as a cheap conflict predictor between parallel sub-agents' patches. If the patches commute, apply them in any order without LLM involvement; that combines naturally with Q3.
- Caveat: agents often produce transiently unparsable code, which triggers mergiraf's line-merge fallback. Run the merge only after per-branch gofmt/parse succeeds.

### Gaps
- No empirical data on structured merge for agent-generated branches.
- Mergiraf's "42% fewer false negatives than Spork" figure comes from an auto-generated page and is unverified.

---

## Q10. Capability-based security and information-flow control for sandboxing / prompt injection

### Takeaway
This is the most mature transplant. CaMeL (capabilities plus control/data-flow separation), FIDES (IFC labels; stops all AgentDojo attacks with policies) and Progent (tool-call privilege DSL; 0% attack success, author-reported) show that deterministic enforcement outside the model works. For janetcode the transplant is taint labels on tool outputs, combined with the existing bwrap/seatbelt sandbox and provenance notes.

### Cited Findings
- FIDES (Microsoft, arXiv 2505.23643) applies deterministic policies over integrity/confidentiality labels of data reaching the LLM and keeps risky tool output in variables. With policies it stops all prompt-injection attacks in its AgentDojo suite; without policy checks all planners succumb. Utility depends on the LLM's ability to use the primitives. — [arXiv 2505.23643](https://arxiv.org/pdf/2505.23643); [github microsoft/fides referenced in summary](https://www.alphaxiv.org/overview/2505.23643v2)
- CaMeL: a Privileged LLM writes control-flow code seeing only the user query; a Quarantined LLM parses untrusted data with no tool access; an interpreter attaches capability metadata (provenance, allowed readers) to every value (secondary summary; the original paper was not fetched). — [aktagon summary](https://signals.aktagon.com/articles/2026/07/camel-defeating-prompt-injection-attacks-in-llm-agents-through-capability-based-security/)
- Progent (arXiv 2504.11703) is a DSL for fine-grained tool-call policies with fallbacks and dynamic updates, LLM-generated from the user query. Reported: attack success rate reduced to 0% on AgentDojo/ASB/AgentPoison while preserving utility. — [arXiv 2504.11703v2](https://arxiv.org/html/2504.11703v2)
- Commentary frames CaMeL as capabilities and FIDES as IFC labels, both under the Biba integrity model. — [danielvaughan out-of-band defences](https://codex.danielvaughan.com/2026/07/08/out-of-band-prompt-injection-defences-camel-fides-progent-codex-cli-sandbox-approval-hooks/)

### Inferences
- Mapping for a coding agent:
  - Label each tool result with integrity: `trusted` (user prompt, repo files at session start, owned by the user) or `untrusted` (web fetch, files from dependencies/vendor dirs, issue text, test output from untrusted code). Labels propagate: a turn whose context contains untrusted data is tainted.
  - Policy (Biba "no write-up"): a tainted turn may not run network-egress bash, modify CI/workflow/credential files, or widen sandbox capabilities without explicit user approval. Untainted turns run normally. This turns the approval prompt from blanket into targeted and reduces approval fatigue.
  - Capabilities rather than ambient authority: each sub-agent gets an explicit capability set (paths writable, network yes/no) materialized as bwrap bind-mounts. The sandbox already provides the enforcement substrate; IFC adds the decision logic.
  - Provenance notes become the label store, a natural fit with janetcode's existing provenance feature.
- Step change: prompt-injection defense becomes deterministic rather than prompt-based, at low utility cost when labels are coarse. For coding, most context is the trusted repo, so most turns stay untainted.

### Gaps
- No evaluation of IFC on coding-agent benchmarks (AgentDojo is mostly workflow/email tasks) was found.
- CaMeL's original paper and numbers were not fetched here.
