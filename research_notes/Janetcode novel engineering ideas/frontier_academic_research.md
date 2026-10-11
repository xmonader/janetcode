# Frontier academic and industry-lab research (2025-2026) on improving LLM software-engineering agents

Scope note: about 22 search/fetch calls. Most figures come from arXiv abstract pages or search snippets, not full-text reads. They are the authors' own numbers unless noted. Each technique is tagged with its year. "Shipping CLI use" is marked as inference unless a source is cited. Target: janetcode (Go single-binary agent that runs cheap models such as DeepSeek V4 flash, Qwen, MiMo, with a repo map, LSP, compaction, memory, a critic, verification gates and step-routing).

## Q1. Test-time compute scaling: parallel sampling + verifier selection, tree search, critics/reward models, execution-free verifiers

### Takeaway
Parallel sampling with a strong selector is where the large, repeatable gains come from (about +7 to +10 pp on SWE-bench Verified for mid-size open models). The selector, not the sampler, is the bottleneck: a plain LLM-as-selector stops improving after about 5-6 candidates. The new 2026 trend is *cheaper* scaling: reusing or branching earlier trajectories, execution-free reward models, and repo-grounded rubrics instead of running tests. MCTS (SWE-Search) helps but costs more to build and is mostly from 2024/25.

### Cited Findings
- **SWE-RM (Dec 2025, arXiv 2512.21919):** an execution-free reward model (MoE, 30B total / 3B active) scores whole trajectories without running tests and picks best-of-N. On SWE-bench Verified it lifts Qwen3-Coder-Flash from 51.6% to 62.0% and Qwen3-Coder-Max from 67.0% to 74.6%. Key finding: two verifiers with nearly identical TTS results can give very different RL outcomes, and calibration/classification accuracy matter beyond best-of-N picking. — [arXiv 2512.21919](https://arxiv.org/abs/2512.21919)
- **SWE-World (Feb 2026, arXiv 2602.03419):** a trajectory verifier used for TTS takes SWR-32B from 55.0 (K=1) to 68.2 (TTS@8), versus 59.4 for the R2E-Gym verifier and 59.6 for the OpenHands verifier (snippet numbers). — [arXiv 2602.03419](https://arxiv.org/pdf/2602.03419)
- **R2E-Gym (Apr 2025):** a *hybrid* of execution-based verifiers (generated tests) and execution-free verifiers (a learned judge) scales best, because the two kinds fail in complementary ways. — [arXiv 2504.07164](https://arxiv.org/pdf/2504.07164)
- **SWE-Replay (Jan 2026, Ding & Zhang, UIUC):** instead of sampling N trajectories from scratch, it archives earlier trials and branches new trials from a "critical intermediate step" (chosen by how much repository exploration the step did, with no LLM judge or value model). It cuts cost by up to 17.4% against naive scaling while matching or improving resolve rate by up to 3.8% on SWE-bench Verified, and generalizes to SWE-bench Pro and Multilingual. Its ablation puts an LLM-as-judge step selector at 54% against SWE-Replay's 60%. — [arXiv 2601.22129](https://arxiv.org/abs/2601.22129); [search snippet PDF](https://arxiv.org/pdf/2601.22129)
- **Agentic Rubrics (Jan 2026, arXiv 2601.04171):** an "expert" agent explores the repo and writes a context-grounded checklist (rubric). Candidate patches are scored against it *without running tests*, so no environment setup is needed. Parallel TTS on SWE-bench Verified reaches 54.2% with Qwen3-Coder-30B-A3B and 40.6% with Qwen3-32B, at least +3.5 pp over the strongest baseline. Rubric scores agree with the ground-truth tests and catch issues the tests miss. The ablation shows agentic context gathering is essential. — [arXiv 2601.04171](https://arxiv.org/abs/2601.04171)
- **Trae Agent (ByteDance, Jul 2025):** treats ensembling as an optimal-solution search with separate generation, pruning and selection agents. It reports 75.20% Pass@1 on SWE-bench Verified and +10.22% on average over four ensemble-reasoning baselines. LLM-as-a-Selector **peaks at 5-6 candidates, then declines**, which motivated an agentic selector. — [arXiv 2507.23370](https://arxiv.org/html/2507.23370v1); [Trae blog](https://www.trae.ai/blog/product_update_0528)
- A replication (TestPrune, Oct 2025) **disabled** Trae's TTS because it "substantially increases execution cost". This is the cost reality of the approach. — [arXiv 2510.18270](https://arxiv.org/pdf/2510.18270)
- **Satori-SWE (May 2025):** evolutionary test-time scaling (iterative self-refinement of samples, built into the model by RL) for sample efficiency without an external verifier. — [arXiv 2505.23604](https://arxiv.org/html/2505.23604v1)
- **SWE-Search (ICLR 2025, older; arXiv Oct 2024):** MCTS plus a Value Agent (numeric and qualitative feedback) plus a Discriminator Agent (multi-agent debate). It reports a 23% relative improvement across five models, and performance scales with search depth. Open source: moatless-tree-search. — [arXiv 2410.20285](https://arxiv.org/pdf/2410.20285)
- **Evaluating Test-Time Scaling of General LLM Agents (2602.18998, v2 Sep 2026):** verifier-guided *revision* gets higher absolute scores, especially on coding, but plateaus. It identifies a "verification gap" that limits parallel scaling. — [arXiv 2602.18998v2](https://arxiv.org/html/2602.18998v2)
- **Overthinking as a free selector (Feb 2025):** across 4,018 SWE-bench Verified trajectories, a higher "overthinking score" (analysis paralysis, rogue actions, premature disengagement) correlates with failure. Choosing the candidate with the *lower* overthinking score improves performance by almost 30% and cuts compute by 43%. — [arXiv 2502.08235](https://arxiv.org/pdf/2502.08235)
- **Terminal-Bench 2.0 (89 tasks):** in an April 2026 snapshot, Codex + GPT-5.5 leads at 82.0%, then ForgeCode + GPT-5.4 at 81.8% and TongAgents + Gemini 3.1 Pro at 80.2%. The same model scores differently under different harnesses, and the median across about 142 submissions is 44%. — [Morph TB2 page](https://www.morphllm.com/terminal-bench-2); [Snorkel](https://www.snorkel.ai/blog/terminal-bench-2-0/); [Codesota](https://www.codesota.com/benchmark/terminal-bench)

### Inferences
- For janetcode with cheap models, the best payoff for the cost is likely **N=3-5 parallel cheap-model rollouts plus a hybrid selector**: (a) execution signal from the repro tests the agent writes (see Q2), (b) a repo-grounded rubric written once per task by the strong slot (Agentic Rubrics), (c) a cheap trajectory-shape heuristic such as the overthinking score. Cap N around 5 when an LLM is the selector (Trae finding).
- SWE-Replay-style **branching from a saved mid-trajectory checkpoint** suits janetcode well, because it already has compaction and session state. Retrying from "after localization" rather than from scratch saves the exploration tokens.
- Shipping CLIs: none of the popular terminal agents (Claude Code, Codex CLI, Aider, OpenCode) is documented, in what I found, as doing parallel N-sampling with verifier selection by default. This is inference, not verified. Trae Agent (open source) and research harnesses (OpenHands, moatless) do it.

### Gaps
- No full-text verification of SWE-World and SWE-RM cost per task (GPU hours for the 30B RM).
- The Terminal-Bench top harnesses (ForgeCode, TongAgents) do not publicly disclose whether they use TTS. Not found.
- SWE-bench Pro top-of-leaderboard techniques in mid/late 2026 were not retrieved, apart from Live-SWE-agent (45.8%) and Confucius (59%, secondary source).

## Q2. Agentic test generation / reproduction-tests-first / differential and property-based verifiers

### Takeaway
Writing a fail-to-pass reproduction test from the issue before patching is now a mature sub-field. The best 2026 open tool reproduces about 66% of SWT-Bench-Verified bugs. These tests are the strongest execution-based signal for selecting among candidates, and they are the "execution" half of the hybrid verifiers that win (R2E-Gym).

### Cited Findings
- **Otter (Feb 2025):** generates tests from the issue text alone to validate patches. Earlier baselines (SWE-Agent+) produced a fail-to-pass test only 19.2% of the time. — [arXiv 2502.05368](https://arxiv.org/html/2502.05368v2)
- **Issue2Test (Mar 2025):** reproduces 84/276 (v1) to 91/276 (v2) SWT-bench-lite instances, reported elsewhere as 32.9%. The versions disagree. — [arXiv 2503.16320v2](https://arxiv.org/html/2503.16320v2)
- **Echo (Mar 2026, arXiv 2603.07326):** code-graph context retrieval plus execution-feedback refinement reaches **66.28% on SWT-Bench-Verified**, described as the best open-source bug-reproduction tool. — [arXiv 2603.07326](https://arxiv.org/html/2603.07326v1)
- **SWT-Bench (2024, older):** a test counts as reproducing the bug only if it fails before the golden patch and passes after it. Code-repair agents beat dedicated test-generation systems at writing these tests. — [SWT-Bench review](https://www.themoonlight.io/es/review/swt-bench-testing-and-validating-real-world-bug-fixes-with-code-agents)
- e-Otter++ reports a 63% average fail-to-pass rate on TDD-Bench Verified (unverified secondary listing). — [search listing](https://arxiv.org/html/2502.05368v2)
- **TestPrune (Oct 2025, "Can Old Tests Do New Tricks"):** reuses or prunes the existing regression test suite to guide issue resolution. — [arXiv 2510.18270](https://arxiv.org/pdf/2510.18270)

### Inferences
- janetcode already refuses a headless run's first write to a test file (per the recent commit log). The research suggests an **inverse, explicit phase**: (1) write a repro test, (2) *execute it on the unpatched code and require that it fails*, (3) patch, (4) require that it passes, plus the existing tests. The "fails before" check is what separates a real repro from a tautological test. Freeze the repro before patching so the patcher cannot edit it.
- Use the repro test as the execution signal in best-of-N selection (Q1), with an execution-free judge as the tie-breaker.

### Gaps
- No 2025-26 primary source found on **property-based or differential testing as SWE-agent verifiers** with measured SWE-bench deltas. Searches did not surface one, so this is a gap rather than a negative result.

## Q3. Learning from experience at inference time: workflow memory, skill libraries, self-improving agents

### Takeaway
Self-modifying scaffolds now work in practice. Live-SWE-agent (Nov 2025) evolves its own tools *during* a run, starting from a bash-only scaffold, and reports 77.4% on SWE-bench Verified and 45.8% on SWE-bench Pro without TTS. Cross-task experience helps only when it is **summarized and correctly selected**: raw trajectories do worse than summaries. Auto-generated repo guidance files often hurt (see Q7).

### Cited Findings
- **Live-SWE-agent (Xia, Wang, Yang, Wei, Zhang; UIUC; Nov 2025):** starts from mini-SWE-agent (bash only) and lets the agent write and refine its own tools/scaffold while it solves the task, with no offline training. It reports 77.4% on SWE-bench Verified without TTS (v3; v1 said 75.4%) and 45.8% on SWE-bench Pro, which it calls best-known at the time. The lab page claims 79.2% on the leaderboard (unconfirmed). Code: github.com/OpenAutoCoder/live-swe-agent. — [arXiv 2511.13646](https://arxiv.org/html/2511.13646v3); [HF page](https://huggingface.co/papers/2511.13646)
- **Darwin Gödel Machine (Sakana, May 2025):** an archive-based open-ended evolution of agent code. It improves from 20.0% to 50.0% on SWE-bench and from 14.2% to 30.7% on Polyglot over about 80 iterations, and beats ablations without self-improvement or without the archive. It is an expensive *offline* process. A secondary source reports reward hacking (fake logs), unverified. — [arXiv 2505.22954](https://arxiv.org/html/2505.22954v3); [Sakana blog](https://sakana.ai/dgm/); [secondary](https://aichats.substack.com/p/darwin-godel-machines-a-self-improving)
- **SWE-ContextBench (Feb 2026, arXiv 2602.08316):** 300 SWE-bench Lite tasks plus 99 related tasks linked through real issue/PR dependencies. Correctly selected *summarized* experience improves resolution and substantially reduces runtime and tokens, especially on harder tasks. Giving the full trajectory ("oracle experience reuse") did worse, at 27.27%. — [arXiv 2602.08316](https://arxiv.org/html/2602.08316v2)
- **SWE-Bench-CL (Jul 2025):** a FAISS semantic memory of summaries of past attempts, queried by the problem statement. — [arXiv 2507.00014](https://arxiv.org/pdf/2507.00014)
- **Confucius Code Agent (Meta/Harvard, Feb 2026):** persistent note-taking across sessions plus a meta-agent build-test-improve loop. It reports 59% Resolve@1 on SWE-bench Pro (secondary awesome-list, not verified). — [awesome-harness-engineering](https://github.com/ai-boost/awesome-harness-engineering)
- "How Well Do Agentic Skills Work in the Wild" (Apr 2026) and DreamBench-SWE (Aug 2026, memory hygiene across sessions) exist, but their results were not retrieved. — [arXiv 2604.04323](https://arxiv.org/pdf/2604.04323); [arXiv 2608.20664](https://arxiv.org/pdf/2608.20664)
- **Speculate with Memory (Jul 2026):** reuses past trajectories to predict the next action and observation, with 19-39% relative accuracy gains on action prediction (see Q5). — [alphaxiv 2607.12236](https://www.alphaxiv.org/abs/2607.12236)

### Inferences
- For janetcode's existing memory: store **distilled summaries keyed by task/repo features**, not raw transcripts, and retrieve selectively (SWE-ContextBench). The "memory hygiene" theme (DreamBench-SWE) suggests stale memory is a real failure mode worth evaluating.
- Live-SWE-agent's on-the-fly tool synthesis is a cheap, revolutionary feature for a Go binary. The agent writes small scripts/tools into a per-repo tool dir that persists across sessions, which turns repeated bash patterns into named tools. It needs sandboxing and review gates.
- DGM-style evolution fits janetcode's eval harness as an *offline* loop (evolve prompts and scaffold config against the evals), not as a runtime feature.

### Gaps
- Agent Workflow Memory (2024) SWE numbers were not verified. Results of the 2026 skill-library papers (SkillRL, Grounded Skill Synthesis 2609.05571) were not retrieved.

## Q4. Context engineering: what context actually helps

### Takeaway
The best-evidenced cheap win is **observation masking**: drop old tool outputs and keep all reasoning and actions. It matches LLM summarization at about half the cost, and a hybrid saves a further 7-11%. Graph-based localization lets small fine-tuned models localize like frontier models. Long context degrades reliability ("context rot"), but the primary numbers come from a vendor.

### Cited Findings
- **The Complexity Trap (JetBrains/TUM, NeurIPS 2025, arXiv 2508.21433):** in SWE-agent on SWE-bench Verified across five model configurations, simple observation masking **halves cost** against the raw agent while matching or slightly beating LLM summarization's solve rate. A hybrid (mask first, summarize rarely) cuts a further 7% against masking alone and 11% against summarization alone. Example: Qwen3-Coder 480B goes from 53.8% to 54.8% with masking (third-party summary). Initial evidence suggests this generalizes to OpenHands. — [arXiv 2508.21433](https://arxiv.org/pdf/2508.21433); [NeurIPS page](https://neurips.cc/virtual/2025/131688)
- **LocAgent (Mar 2025):** builds a heterogeneous code graph (files, classes, functions, imports, invocation and inheritance edges) and lets the agent traverse it for localization. It reaches up to 92.7% file-level accuracy (32B). A fine-tuned Qwen2.5-7B is roughly on par with GPT-4o at about $0.05 per example against $0.66-0.79 for Claude-3.5, about 15x cheaper. The *unfine-tuned* 7B does poorly because of weak tool use. — [arXiv 2503.09089](https://arxiv.org/pdf/2503.09089); [emergentmind](https://www.emergentmind.com/topics/locagent)
- **Context rot (Chroma, 2025):** reliability drops as input grows even on simple tasks, across 18 models. Related-but-irrelevant distractors amplify errors. Secondary sources report 20-50% drops from 10k to 100k+ tokens on NIAH variants, and the reported magnitudes disagree. The source is a vendor with a RAG interest. — [W&B summary](https://wandb.ai/byyoung3/ml-news/reports/Chroma-Research-Warns-of-Context-Rot-as-LLMs-Falter-with-Long-Inputs--VmlldzoxMzczMjQ4MQ); [Hamel notes](https://hamel.dev/notes/llm/rag/p6-context_rot.html)
- **Scout handoffs (SuperScout, Aug 2026):** a 7B "searcher" explores the repo and writes a structured handoff. Reproduction claims in the handoff are checked in a sandbox and false ones are deleted. The handoff, not the routing, drove the gains (see Q5). — [arXiv 2608.04804](https://arxiv.org/abs/2608.04804)

### Inferences
- If janetcode compaction is LLM-summary-first, switching to **masking-first** (elide old tool observations with a stub such as "[output of `go test` elided, 340 lines, exit 1]") and summarizing only on overflow should cut tokens roughly in half with no success loss. This is the most strongly evidenced single change.
- The repo map + LSP already approximates LocAgent's graph. The finding that matters is that *weak models need the graph exposed as traversal tools* (neighbors, callers, inheritors) with compact outputs, not as a large dump.
- **Verify the facts in a handoff/summary before passing it on** (SuperScout sandbox-checks repro claims). This matters for janetcode's step routing between slots, because hallucinated "I confirmed X" claims spread to the next model.

### Gaps
- Chroma's primary report was not fetched, so the context-rot numbers are secondary.
- No 2026 controlled study found comparing repo-map styles (Aider-style PageRank map against graph tools) on SWE-bench.

## Q5. Small/cheap model amplification

### Takeaway
The strongest 2026 result for cheap models is that **a high-quality handoff from a cheap scout** lets cheaper fixers match the best single frontier model at about one-fifth of the cost per solve. Routing itself added little. Speculative actions (ICLR 2026) give lossless latency cuts by having a fast model guess tool calls. Recent small open models (3B-active) reach about 70% on SWE-bench Verified, so the harness matters relatively more.

### Cited Findings
- **SuperScout / "Scrouting" (Aug 2026, arXiv 2608.04804):** a 7B scout explores, writes a sandbox-verified handoff, and a router picks one of four frontier fixers. On SWE-bench Pro (Python, 266 tasks) it solves 159 against 158 for the best single model, at about **1/5 the cost per solve**. The scout adds under half a cent of GPU time per task. Key ablation: **always using the cheapest fixer with the handoff ties the routed system**, so the handoff drives the result. The handoff lifts the three cheaper fixers and *slightly hurts the strongest*. — [arXiv 2608.04804](https://arxiv.org/abs/2608.04804)
- Other 2026 coding-agent routers exist (SWE-Router 2607.00053, CodeRescue 2607.19338, TRACE-Router 2607.22465), titles only and results not retrieved. — [search listing](https://arxiv.org/pdf/2608.04804)
- **Speculative Actions (Columbia, ICLR 2026 oral):** a faster model predicts the next actions (tool calls, MCP requests, LLM calls) and runs them in parallel with the slow model's step, committing only on a match, so it is lossless. Evaluated on gaming, e-commerce and web search, plus a lossy OS variant. — [arXiv 2510.04371](https://arxiv.org/pdf/2510.04371); [ICLR](https://iclr.cc/virtual/2026/oral/10009727)
- **Speculate with Memory (Jul 2026):** adds trajectory memory to the speculator. It reports 19-39% relative gains in action-prediction accuracy and up to 2.5x on observation prediction in repetitive action spaces, and stays lossless (speculation uses idle time). — [alphaxiv 2607.12236](https://www.alphaxiv.org/abs/2607.12236)
- Small open models: Qwen3-Coder-Next reports 70.6% on SWE-bench Verified with 3B active out of 80B, and Devstral-Small-2-24B reports 68.0% (vendor-reported, harness-dependent). — [Modal guide](https://modal.com/resources/best-open-source-models-swe-bench-coding-agents)
- Agentic Rubrics and SWE-RM show *small* verifiers (3B-active RM; rubrics with 30B-A3B) adding 7-10 pp to cheap policies (see Q1). — [arXiv 2512.21919](https://arxiv.org/abs/2512.21919); [arXiv 2601.04171](https://arxiv.org/abs/2601.04171)
- Industry practice: Augment's Prism puts a small, fast planner model in front of a model pool to choose per turn (vendor blog). — [Augment routing guide](https://www.augmentcode.com/guides/ai-model-routing-guide)

### Inferences
- For janetcode step-routing: invest in a **structured, fact-checked scout → fixer handoff** (files, symbols, repro command, verified failing output) rather than a more elaborate routing policy. SuperScout's ablation suggests the router is nearly worthless once the handoff is good. Note the hint that handoffs can slightly hurt the strongest model, so make them optional context for a strong slot.
- Speculative execution of **read-only tools** (grep, read file, `go build`, LSP queries) predicted by the cheap slot while the main slot thinks is lossless for side-effect-free tools, and cuts wall-clock time. To my knowledge no shipping CLI does this (inference).

### Gaps
- No 2025-26 paper retrieved on **constrained decoding for edits** (grammar-constrained diff/patch formats) with SWE-bench deltas. Not found in this pass.
- Speculative Actions has not been evaluated on SWE-bench-style coding in what I retrieved.

## Q6. Formal methods + LLMs for code

### Takeaway
Agentic verifier-in-the-loop generation is strong in Dafny (about 93% on DafnyBench) and weak in Lean (5% proof success on VERINA). Repo-level verified editing is only now being benchmarked (Vero, Aug 2026). For a general Go coding agent the practical takeaways are indirect: use cheap, sound checkers (type checker, `go vet`, staticcheck, race detector) as a verifier-guided repair loop. Full verification is still not practical for arbitrary repos.

### Cited Findings
- **AxDafny / DafnyProver (Jun 2026; ICML 2026):** verifier-guided agentic repair of implementations, invariants, assertions and termination arguments reaches **92.7% on DafnyBench**, +6.5 pp over the previous proof-hint baseline. Verification success and runtime test pass measure *different* things. — [arXiv 2606.32007](https://arxiv.org/abs/2606.32007); [ICML 2026](https://icml.cc/virtual/2026/82473)
- **Vericoding benchmark (Sep 2025):** off-the-shelf LLMs reach 27% in Lean, 44% in Verus/Rust and 82% in Dafny. Pure Dafny verification improved from 68% to 96% in one year. — [Kavli preprint](https://preprints.kavlimeetings.org/2025/09/26/a-benchmark-for-vericoding-formally-verified-program-synthesis)
- **VERINA (Lean, ICLR 2026):** o3 scores 72.6% on code correctness, 52.3% on spec soundness/completeness and only 4.9% on proof success. — [ICLR 2026](https://iclr.cc/virtual/2026/poster/10011965)
- **AlgoVeri (2026):** 77 identical problems in Dafny, Verus and Lean show a large gap between verification paradigms. — [emergentmind](https://www.emergentmind.com/topics/algoveri)
- **Vero (Aug 2026):** 43 repo-level multi-module instances (Python, Dafny, Verus, Coq). Agents can "audit" by proving a spec unsatisfiable or the reference code wrong. — [chatpaper listing](https://chatpaper.com/chatpaper/paper/333057)

### Inferences
- The transferable pattern is the **verifier-guided repair loop with a sound, fast oracle**. For Go: compile, `go vet`, staticcheck, `-race` tests, and possibly gopls diagnostics as gates, all of which janetcode largely has. Spec inference (LLM-written assertions or contracts checked by fuzzing, e.g. Go native fuzzing) is a plausible bridge, but I found no measured SWE evidence for it.

### Gaps
- No practical 2025-26 study on **LLM spec inference used to verify edits in mainstream languages** (Go, Python) with SWE-bench outcomes.

## Q7. Surprising negative results

### Takeaway
Several "obvious" improvements hurt or do nothing: LLM summarization against masking, auto-generated AGENTS.md files, LLM-as-selector beyond about 5 candidates, full trajectories as memory, more thinking in agentic loops, and routing once a good handoff exists.

### Cited Findings
- **LLM summarization is no better than dropping old observations**, and costs more. — [arXiv 2508.21433](https://arxiv.org/pdf/2508.21433)
- **Repository context files (AGENTS.md) (Gloaguen et al., ETH, arXiv 2602.11988, Feb 2026):** they do not generally improve success and raise inference cost by more than 20%. *LLM-generated* files reduce success by about 2-3%, and developer-written ones help about 4% (secondary write-ups). Agents follow the files literally: a tool named in the file was used 160x more. In contrast, Lulla et al. (2601.20404) report curated AGENTS.md files cut runtime by 28.6% and output tokens by 16.6% on focused PRs (efficiency, not correctness). — [Vaughan synthesis](https://codex.danielvaughan.com/2026/07/26/do-auto-generated-agents-md-files-actually-help-codex-cli-init-research-evidence-context-engineering/); [secondary summary](https://ai-tools-aggregator-seven.vercel.app/blog/2026-02-17-agents-md-evaluation-study/); [arXiv 2601.20404](https://arxiv.org/html/2601.20404v2); also Probe-and-Refine tuning of guidance [arXiv 2606.20512](https://arxiv.org/pdf/2606.20512)
- **LLM-as-selector degrades past 5-6 candidates.** — [Trae](https://arxiv.org/html/2507.23370v1)
- **Full trajectories as experience do worse than summaries** (27.27% in the oracle full-trajectory setting). — [arXiv 2602.08316](https://arxiv.org/html/2602.08316v2)
- **Overthinking:** reasoning models lean toward internal reasoning over acting, and higher overthinking correlates with failure. — [arXiv 2502.08235](https://arxiv.org/pdf/2502.08235)
- **Routing adds about nothing over a good handoff plus the cheapest fixer**, and handoffs slightly hurt the strongest fixer. — [arXiv 2608.04804](https://arxiv.org/abs/2608.04804)
- **A verifier that is good at TTS is not necessarily good for RL** (calibration matters). — [arXiv 2512.21919](https://arxiv.org/abs/2512.21919)
- **Verifier-guided revision plateaus,** and a "verification gap" caps parallel scaling. — [arXiv 2602.18998](https://arxiv.org/html/2602.18998v2)
- Harness effects are large: the same model scores differently across Terminal-Bench harnesses. One aggregator argues the opposite (model choice matters more than the scaffold). — [Codesota](https://www.codesota.com/benchmark/terminal-bench); [Benchmarklist](https://benchmarklist.com/benchmarks/vals_terminal_bench_2/)

### Inferences
- Candidate janetcode actions, ranked by how strong the evidence is against cost: (1) masking-first compaction; (2) repro-test-first with a "must fail before" check; (3) N=3-5 parallel cheap rollouts with a hybrid selector (repro-test pass, rubric from the strong slot, overthinking heuristic); (4) a verified scout handoff in place of complex routing; (5) on-the-fly tool synthesis (Live-SWE-agent); (6) speculative read-only tool execution; (7) summarized and selectively retrieved cross-run memory. Keep any auto-generated project guidance short, because long or auto-generated guidance costs tokens and can lower success.

### Gaps
- Most numbers are author-reported on SWE-bench Verified with Python repos. Transfer to Go repos and to the specific cheap models janetcode uses (DeepSeek V4 flash, MiMo) is untested in this literature.
- Several 2026 papers (routers, skill libraries, DreamBench-SWE) were identified only by title.
