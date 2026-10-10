# Context Engineering, Long-Horizon Memory, and Token/Latency/Cost Efficiency for Coding Agents (2025-2026)

Scope: mechanisms janetcode (a single-binary Go terminal coding agent with compaction, repo map, memory, skills, LSP and model-slot step routing) could adopt to cut cost and latency or to extend its effective horizon. It covers cheap OpenAI-compatible models (DeepSeek V4, Qwen, MiMo, Kimi) and Anthropic. Research date: 2026-10-10.

Note on source quality: some model and version names below (for example "qwen3.8-max", "Opus 4.8", "Kimi K3", "GPT-5.6") appear exactly as the fetched provider pages or search results gave them. I copied them as given and did not verify them independently.

## Q1. Prompt caching mechanics across providers, and agent designs that maximize hit rate

### Takeaway
Every relevant provider now caches prompt prefixes, and cache hits cost 2.5-50x less than misses. A coding agent's input-to-output token ratio is around 100:1, so its cost is set mostly by cache hit rate. The design rules are the same for every provider:
- Keep the prefix (tools, then system, then history) byte-stable.
- Only append to the context.
- Serialize deterministically.
- Never edit earlier turns.

The main difference is that Anthropic and the Qwen explicit cache need explicit breakpoints and charge for cache writes. DeepSeek, Kimi, OpenAI and the Qwen implicit cache are automatic and best-effort.

### Cited Findings
**Why caching dominates cost**
- Manus calls KV-cache hit rate "the single most important metric for a production-stage AI agent." Its agents run at about a 100:1 input:output token ratio, and an average task takes about 50 tool calls. On Claude Sonnet, cached input costs $0.30/MTok versus $3/MTok uncached (10x) — [Manus blog](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus)

**Manus practices for a high hit rate**
- No timestamp at the start of the system prompt.
- Append-only context.
- Deterministic JSON serialization, because some libraries do not guarantee key order.
- Explicit cache breakpoints at least at the end of the system prompt.
- With vLLM: enable prefix caching and route requests by session ID.
- Source: [Manus blog](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus)
- "Mask, don't remove" tools. Adding or removing tools mid-task invalidates the cache, because tools sit at the front of the prompt. Manus instead keeps the tool set fixed and constrains which actions are allowed with logit masking or response prefill (Auto / Required / Specified modes). Shared name prefixes (`browser_`, `shell_`) make group-level masking easy — [Manus blog](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus)

**Anthropic**
- Breakpoints and modes:
  - Up to 4 `cache_control` breakpoints per request.
  - A new top-level "automatic" `cache_control` places the breakpoint on the last cacheable block and moves it forward as the conversation grows. It uses one of the 4 slots.
  - Source: [Anthropic prompt caching docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- Lifetime and pricing:
  - Default TTL is 5 minutes, refreshed for free on each hit. `ttl: "1h"` is available.
  - Writes cost 1.25x base input (5m) or 2x (1h). Reads cost 0.1x for most models, 0.05x on Opus 5.5 and Sonnet 5.5, and 0.025x on Fable 5.1 and Mythos 5.1.
  - Cache hits do not count against rate limits.
  - Source: [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- Minimum cacheable prefix length:
  - 512 tokens on the newest models (Opus 5.5, Sonnet 5.5, Haiku 5.5, and others).
  - 1,024 / 2,048 / 4,096 tokens on older models. For example, Haiku 4.5 and Opus 4.5 need 4,096.
  - A shorter prompt is silently not cached.
  - Source: [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- Lookback rule:
  - The cache looks back at most 20 block positions from a breakpoint.
  - A run of consecutive `tool_use` blocks, or of `tool_result` blocks, counts as one position.
  - If a growing conversation pushes the breakpoint 20+ blocks past the last cache write, add a second breakpoint closer to it.
  - Source: [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- What invalidates the cache (the hierarchy is tools → system → messages):
  - Changing tool definitions invalidates everything.
  - Changing `tool_choice` or adding/removing images invalidates the message cache.
  - Changing thinking or effort parameters invalidates the message cache.
  - Source: [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
- Agent guidance from the same docs:
  - Inject mid-conversation `{"role":"system"}` messages instead of editing the top-level system field.
  - Use stable JSON key order in `tool_use` inputs.
  - For parallel requests, wait until the first response starts streaming before sending the rest.
  - Pre-warm the cache with `max_tokens: 0`.
  - Use the 1-hour TTL when gaps between requests exceed 5 minutes.
  - Use cache diagnostics to find where consecutive requests diverge.
  - Source: [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)

**DeepSeek**
- Mechanics:
  - Caching is on by default and backed by disk.
  - A hit requires a full match on a persisted "cache prefix unit." These units are cut at the end of each user input and model output, at common prefixes seen across requests, and at fixed token intervals in long inputs.
  - Best-effort: no 100% hit guarantee.
  - Unused cache entries are cleared within "a few hours to a few days."
  - The response reports `prompt_cache_hit_tokens` and `prompt_cache_miss_tokens`.
  - Source: [DeepSeek KV cache guide](https://api-docs.deepseek.com/guides/kv_cache)
- Pricing (per 1M tokens):

  | Model | Cache hit | Cache miss | Output |
  |---|---|---|---|
  | deepseek-flash (V4.1-Flash) | $0.003 off-peak / $0.006 peak | $0.15 / $0.30 | $0.60 / $1.20 |
  | deepseek-v4-pro | $0.022 / $0.044 | $0.66 / $1.32 | $1.98 / $3.96 |

  - A hit costs about 2% of a miss, roughly 50x cheaper.
  - Off-peak is half price. Peak is 01:00-04:00 and 06:00-10:00 UTC on weekdays.
  - Context is 1M tokens; output up to 384K.
  - Source: [DeepSeek pricing](https://api-docs.deepseek.com/quick_start/pricing)

**Qwen / DashScope (Alibaba Model Studio)**
- Two modes: implicit (automatic, always on) and explicit. Both need at least 1,024 tokens.
- Implicit cache:
  - Hits are typically billed at 20% of input price. It varies by model: 10% for deepseek-v4.1-flash hosted there, 25% for glm-5.2.
  - No fixed TTL, and hits are not guaranteed.
- Explicit cache:
  - Uses Anthropic-style `cache_control: {"type":"ephemeral"}`, up to 4 markers, and a 20-block lookback.
  - Creation costs 125% of input price; hits cost 10%; TTL is 5 minutes, reset on each hit.
  - Tool definitions are cached only as part of the system message.
- The two modes are mutually exclusive.
- Source: [Alibaba Cloud Model Studio context cache](https://www.alibabacloud.com/help/en/model-studio/context-cache)

**OpenAI**
- Caching is automatic for prompts of 1,024+ tokens.
- `prompt_cache_key` is a routing hint: requests with the same key go to the same backend, which raises the hit rate.
- Retention:
  - Before GPT-5.6, `prompt_cache_retention` takes `in_memory` (about 5-10 minutes, capped at 1h) or `24h`.
  - From GPT-5.6, `prompt_cache_options.ttl` takes only `30m`, and cache writes are billed.
- Source: [OpenAI prompt caching guide](https://developers.openai.com/api/docs/guides/prompt-caching); write-billing detail from [Azure OpenAI docs](https://learn.microsoft.com/en-my/Azure/foundry/openai/how-to/prompt-caching)

**Kimi / Moonshot (third-party sources only)**
- Caching is reported as automatic and prefix-based. It has no cache ID or TTL controls, and hits are reported in `usage.cached_tokens` — [Verdent guide](https://www.verdent.ai/guides/agents/kimi-k3-api-guide)
- Reported prices per 1M tokens:
  - K2.7 Code: $0.19 hit / $0.95 miss / $4.00 output (described as promotional).
  - K3: $0.30 hit / $3.00 miss / $15.00 output.
  - Sources: [puter Kimi pricing](https://developer.puter.com/tutorials/kimi-api-pricing/), [Verdent](https://www.verdent.ai/guides/agents/kimi-k3-api-guide)
  - These are unverified against official Moonshot docs.

### Inferences
- **Treat context as a log.** janetcode should serialize the conversation as an append-only log of byte-stable records, with deterministic JSON and a fixed tool order. It should never rewrite old turns in place. This one design maximizes hits on all five providers at once.
- **Compaction is a cache-economics event, not just a context-limit event.** Each compaction rewrites the prefix and causes a full cache miss. At DeepSeek prices, a miss costs about 50x a hit, so compacting rarely but decisively probably beats frequent incremental rewrites. Any masking or pruning (Q2) should be applied in large batches at the same points where compaction already breaks the cache, not every turn.
- **Model-slot routing breaks caches across models.** Every provider caches per model (Qwen states this explicitly). Switching slots mid-task means paying full price for the whole prefix on the new model. A router should count that re-prefill cost.
- **Tool-set changes in step routing are cache-busters.** If different steps expose different tool sets, janetcode should instead keep one fixed tool list and enforce restrictions through the system message, a mid-conversation system note, or `tool_choice`. Note that on Anthropic, changing `tool_choice` still invalidates the message cache.
- **Use the cache fields as telemetry.** janetcode should log `cache_read_input_tokens` / `prompt_cache_hit_tokens` / `cached_tokens` every turn and alert when the hit ratio drops. This is cheap, and it catches regressions such as timestamps or nondeterministic map iteration in Go JSON (Go's `encoding/json` sorts map keys, but struct field and slice order must also stay stable).
- **Schedule batch work off-peak on DeepSeek.** Benchmarks and other non-interactive runs get a straight 50% discount.
- **Use OpenAI-compatible routing hints.** On OpenAI, set `prompt_cache_key` to the session ID. On vLLM or other self-hosted servers, use session-affinity routing.

### Gaps
- No official Moonshot caching docs were retrieved, so Kimi mechanics and prices come from third parties.
- Xiaomi MiMo API caching was not researched; no source was found in this pass.
- OpenAI's exact cache-read discount was not retrieved.
- DeepSeek's prefix-unit interval size is not documented.
- I found no published measurements of real hit rates for coding agents on DeepSeek or Qwen.

## Q2. Context compression: observation masking vs summarization, tool-output pruning, context rot, notes, sub-agents

### Takeaway
On SWE-bench, simply masking old tool outputs (keeping the last ~10 turns verbatim) halves cost and matches LLM summarization's solve rate. Summarization also makes trajectories about 13-15% longer.

Learned line-level pruners of tool output (SWE-Pruner) cut 23-54% of tokens with equal or better success. Read operations account for about 76% of tokens, so pruning file reads is the biggest lever.

Context rot is real and holds across all 18 models Chroma tested: focused ~300-token prompts beat full ~113k-token prompts by a wide margin. The goal is therefore the smallest set of high-signal tokens.

### Cited Findings
**JetBrains "The Complexity Trap" (arXiv 2508.21433)**
- Setup: SWE-agent on SWE-bench Verified (n=500) with five model configurations. Solve rate and cost per instance (raw → masking → LLM-summary):

  | Model | Raw agent | Observation masking | LLM summary |
  |---|---|---|---|
  | Qwen3-Coder 480B | 53.4%, $1.29 | 54.8%, $0.61 (-52.7%) | 53.8%, $0.64 |
  | Gemini 2.5 Flash | 32.8%, $0.41 | 35.6%, $0.18 (-56.1%) | 36.0%, $0.24 |
  | Qwen3-32B | 17.0%, $1.12 | 15.0%, $0.55 | 16.0%, $0.50 |
  | Gemini 2.5 Flash (thinking) | 40.4% | 36.4% | 31.4% |

  - Source: [arXiv 2508.21433 HTML](https://arxiv.org/html/2508.21433)
- Masking setup: the masking window M=10 worked best. The LLM-summary setup summarized N=21 turns once the history reached 31 turns, keeping the last 10 turns verbatim — [arXiv 2508.21433](https://arxiv.org/html/2508.21433)
- Summarization lengthens trajectories:
  - Gemini 2.5 Flash averaged 52 turns with summarization versus 44 with masking (about 15% longer).
  - Qwen3-Coder ran about 15% longer than the raw agent.
  - The summarization calls themselves cost 0.65-7.2% of total cost.
  - Source: [arXiv 2508.21433](https://arxiv.org/html/2508.21433)
- Hybrid (masking plus occasional summarization, N=43, M=W=10): +2.6pp solve rate over the raw agent, 7% cheaper than masking, and 11% cheaper than summarization. Tested only on Qwen3-Coder on Verified-50 — [arXiv abs](https://arxiv.org/abs/2508.21433)

**Tool-output pruning**
- SWE-Pruner (arXiv 2601.16746):
  - A 0.6B "skimmer" keeps lines relevant to a goal the agent states.
  - Cuts 23-54% of tokens on SWE-bench Verified while improving success.
  - In a Mini-SWE-Agent analysis, read operations were 76.1% of token consumption.
  - Source: [SWE-Pruner](https://arxiv.org/html/2601.16746v1)
- SWE-Pruner Pro (arXiv 2607.18213):
  - A small head on the agent's own hidden states labels each line keep or prune.
  - Saves up to 39% of prompt and completion tokens.
  - Raised the SWE-bench Verified resolve rate by +3.8% on MiMo-V2-Flash.
  - Abstract-level only — [HF papers](https://huggingface.co/papers/2607.18213)
- Squeez (arXiv 2604.04979): a fine-tuned 2B model for tool-output extraction, reported at 92% input compression with 0.80 F1. Unverified, from an aggregator — [Emergent Mind](https://www.emergentmind.com/papers/2604.04979)
- Pichay reportedly finds 21.8% "structural waste" tokens in production agent sessions. This is secondhand — [Codex CLI blog](https://codex.danielvaughan.com/2026/06/17/context-pruning-research-swe-pruner-pichay-contextbudget-codex-cli-token-management-compaction/)

**Context rot (Chroma, July 2025)**
- Across 18 LLMs, performance degraded as input length grew in every experiment. Degradation was steeper when the needle and question had low semantic similarity — [Chroma](https://www.trychroma.com/research/context-rot)
- Distractors: one distractor hurt accuracy, and four hurt more — [Chroma](https://www.trychroma.com/research/context-rot)
- Shuffled (incoherent) haystacks consistently beat coherent ones — [Chroma](https://www.trychroma.com/research/context-rot)
- LongMemEval (306 prompts): every model scored much higher with focused prompts (~300 tokens) than with full prompts (~113k tokens). Thinking modes narrowed the gap but did not close it — [Chroma](https://www.trychroma.com/research/context-rot)

**Anthropic practice**
- Compaction in Claude Code keeps the summary plus the 5 most recently accessed files.
- Tune the compaction prompt for recall first, then for precision.
- Tool-result clearing is "the lightest touch" form of compaction.
- Sub-agents may explore with tens of thousands of tokens but return condensed summaries of 1,000-2,000 tokens.
- Structured notes (NOTES.md, to-do lists) carried a Pokémon agent across 1,234 steps.
- Source: [Anthropic effective context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)

**Manus practice**
- Use the file system as restorable compression: drop the content but keep the URL or path so the agent can re-read it.
- Recite goals by rewriting a todo.md at the end of the context.
- Keep errors and stack traces in context so the model does not repeat mistakes.
- Add controlled variation to serialization, because repetitive action/observation pairs cause pattern-copying drift.
- Source: [Manus blog](https://manus.im/blog/Context-Engineering-for-AI-Agents-Lessons-from-Building-Manus)

### Inferences
- **Make masking-first compaction the default and summarization the fallback.** Replace tool outputs older than about 10 turns with a one-line placeholder that is still restorable (path, line range, exit code, byte count), and keep the agent's own reasoning and actions. Summarize only when masking cannot get under the budget (the hybrid result).
- **Apply masking in blocks for cache-friendliness.** Masking every turn shifts the "last 10" window and rewrites the prefix on every turn, which destroys cache hits. Mask in chunks, for example every K turns or when uncached tokens exceed a threshold, so the cache is broken rarely. Note that the Complexity Trap cost figures probably did not model provider caching, so the real savings with caching could differ (unverified).
- **Prune file reads at read time.** Reads are about 76% of tokens. Cheap, non-learned approximations of SWE-Pruner:
  - Use the LSP and repo map to return the enclosing symbol or function rather than the whole file.
  - Truncate with head/tail plus a "N lines omitted, re-read with range" marker.
  - Deduplicate repeated reads of an unchanged file by referring to the earlier turn.
- **Keep failing test output, but compress it.** Keep the error and the first failing assertion; drop passing-test noise. This follows Manus's "keep errors" rule.
- **Use sub-agents to isolate exploration.** Codebase search done in a sub-agent that returns a 1-2k token summary keeps the main context focused. That matters more for weaker open models, given the context-rot findings.

### Gaps
- I found no study that measures masking versus summarization while accounting for provider prompt-cache pricing.
- LLMLingua-style token-level compression applied to coding agents was not found in this pass. Token-level deletion also likely breaks code syntax and caching (inference, unverified).
- SWE-Pruner Pro and Squeez numbers are abstract or aggregator-level only.

## Q3. Codebase knowledge representation: grep vs embeddings vs graphs

### Takeaway
The evidence is split and comes mostly from vendors. Claude Code dropped a local vector DB in favour of agentic grep and glob search because it was simpler and worked better. Cursor reports +12.5% average answer accuracy from adding a semantic search tool trained on agent traces on top of grep. The consensus is a hybrid: grep first, with semantic or structural retrieval as an additional tool.

### Cited Findings
- Cursor reports that adding semantic search to a grep-based agent raised accuracy by 12.5% on average, with a range of 6.5-23.5% depending on the model. Its embedding model is trained on agent sessions (searches and file opens made before the right code was found). Cursor concludes that grep plus semantic search together is best. Measured on internal Cursor Context Bench — [Cursor blog](https://cursor.com/blog/semsearch)
- Boris Cherny: early Claude Code used a local vector database, but iterative agentic search proved simpler and worked better — [AI Engineer talk](https://www.ai.engineer/talks/UM6sFg_jdlE-rag-is-dead-right-kuba-rogut) (secondhand summary)
- Anthropic: Claude Code loads CLAUDE.md up front and uses glob and grep just in time, avoiding stale indexes and complex syntax trees — [Anthropic context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- A GrepRAG benchmark reportedly found grep beat several semantic and graph retrieval methods for repository-level code-completion retrieval. Secondhand via search summary — [Morph semantic code search page](https://morphllm.com/semantic-code-search)

### Inferences
- janetcode already has a repo map and LSP. Those structural tools (go-to-definition, references) are deterministic and stay fresh, so they probably give most of the value of a code graph without an embedding index.
- Adding embeddings would add an indexing step, staleness problems and binary size, against an uncertain gain for cheap models. A lighter alternative is to rank grep hits by repo-map centrality.

### Gaps
- I found no independent, controlled benchmark comparing SCIP/LSIF graphs, embeddings and grep in agent loops.
- DeepWiki-style hierarchical summaries and learned localization were not evaluated in this pass.

## Q4. Cross-session / organizational memory: what works, what poisons agents

### Takeaway
File-based memory that is loaded at session start (CLAUDE.md / AGENTS.md style) is effective but is also the main poisoning surface. Poisoned memory persists across sessions far more when it is injected into the system prompt (about 65% in one study) than when it is retrieved on demand through a search tool (about 17%).

### Cited Findings
- Anthropic: structured notes kept outside the context window, plus a file-based memory tool (beta with Sonnet 4.5), support long-horizon tasks — [Anthropic](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)
- "Bad Memory" (arXiv 2607.14611): coding agents persist memory in plain-text files (CLAUDE.md, AGENTS.md) that are loaded at session start and that the agent itself can edit. That self-editing loop is the attack surface — [arXiv 2607.14611](https://arxiv.org/pdf/2607.14611)
- "From Untrusted Input to Trusted Memory" (arXiv 2606.04329):
  - Poisoned instructions written in one session kept influencing later sessions: about 64.7% on HERMES, which injects memory into the system prompt, versus about 17.4% on OpenClaw, which retrieves memory via a search tool.
  - Agents that write and retrieve memory more freely are easier to poison.
  - Source: [arXiv 2606.04329](https://arxiv.org/pdf/2606.04329)
- eTAMP (web agents): one contaminated page poisoned memory, with attack success up to 32.5% on GPT-5-mini. Stressed agents were more susceptible — [alphaXiv 2604.02623](https://alphaxiv.org/abs/2604.02623)
- MemSecBench tracks poisoned memory from the write through to its consequence and repair — [arXiv 2607.27080](https://arxiv.org/pdf/2607.27080)

### Inferences
- **Load less memory up front.** Keep the always-loaded memory small and human-curated. Make agent-written memories retrieval-only, gated by a tool call rather than injected into the system prompt.
- **Record provenance.** Tag each memory with its source (user versus agent versus tool output), never auto-write memory derived from tool output or web content, and require user confirmation to promote a memory to always-loaded.
- **Cache benefit.** Small, rarely changing always-loaded memory also keeps the cached prefix stable.

### Gaps
- I found no quantitative study showing how much cross-session memory improves coding-agent success. The evidence found here is about risk, not benefit.

## Q5. Latency: speculative and early tool execution, edit formats, fast apply

### Takeaway
Speculative tool execution, where tools start before or while the model is still generating, is an active 2026 research area. Reported gains are 1.65x end-to-end and a 48.5% cut in task time. Tool execution takes 16-37% of wall time.

Fast-apply models (Morph, Relace) apply edits at about 10k tokens/s. They claim about 35% faster end-to-end completion than search/replace, but these are vendor claims only.

### Cited Findings
- PASTE ("Act While Thinking", arXiv 2603.18897): exploits predictable tool-call sequences and parameter dependencies to run tools ahead of time. Reports a 48.5% reduction in average task completion time and 1.8x tool throughput — [arXiv 2603.18897](https://arxiv.org/html/2603.18897)
- SPORK (arXiv 2607.03333): a forked probe sharing the KV prefix predicts the tool call early. Tool execution is 16-37% of end-to-end wall time — [alphaXiv 2607.03333](https://www.alphaxiv.org/abs/2607.03333)
- SPAgent (arXiv 2511.20048): up to 1.65x end-to-end speedup for search agents — [arXiv 2511.20048](https://web3.arxiv.org/pdf/2511.20048)
- Speculative Macro Commit (arXiv 2609.03236): multi-step drafts of actions, committed when the actor's next call matches. Reports 10.23% lower latency than the Speculative Actions baseline on τ²-Bench and 7.7% on AppWorld — [arXiv 2609.03236](https://arxiv.org/pdf/2609.03236)
- Morph v3-fast:
  - 10,500+ tokens/s on a single B200 (up from 4,500).
  - Claims about 35% faster end-to-end completion than search/replace. Accuracy is cited as 98% or 96% in different places.
  - $0.80 input / $1.20 output per 1M tokens.
  - Sources: [Morph blog](https://www.morphllm.com/blog/morph-breaks-10k-barrier.md), [OpenRouter](https://openrouter.ai/compare/morph/morph-v3-fast/relace/relace-apply-3)
- Relace Apply 3: 10k+ tokens/s, 256k context, $0.85 input / $1.25 output per 1M tokens — [OpenRouter](https://openrouter.ai/compare/morph/morph-v3-fast/relace/relace-apply-3)
- Anthropic docs: for parallel requests, send the first, wait until it starts streaming, then send the rest, so they hit the cache — [Anthropic docs](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)

### Inferences
- **Speculative tool execution, cheapest win.** Start read-only tools (file read, grep, LSP lookup) as soon as a streamed tool-call's arguments have been parsed, before the model finishes the rest of its turn. Run independent read-only parallel calls concurrently. Never speculate on writes or shell commands.
- **Prefetch from patterns.** After an edit, the next action is predictably a test or build, so warm it (for example `go build` cache or vet) in the background (PASTE-style).
- **Fast-apply is optional.** A fast-apply model is an external paid dependency. For janetcode, search/replace edits with a fuzzy-match fallback likely stay competitive. Fast-apply mainly helps when a cheap model emits lazy "... existing code ..." edits.

### Gaps
- I found no independent benchmark of edit formats (search/replace vs whole file vs apply model) on 2026 open models.
- Constrained decoding for tool calls via OpenAI-compatible APIs was not covered in this pass.

## Q6. Model routing / cascades per step

### Takeaway
Routing on a partial trajectory beats routing on the task description: let a cheap model explore for a few turns, then escalate based on predicted success (SWE-Router, ICML 2026). Vendor routers report 39-66% cost savings at near-frontier quality on SWE tasks, but none of these figures is independently replicated.

### Cited Findings
- SWE-Router (arXiv 2607.00053):
  - A cheap model runs a few exploratory turns, then a value head predicts success and escalates if needed.
  - The paper argues that the task description alone has a routing error floor, and states a Bayes-optimality theorem that conditioning on the partial trajectory never hurts.
  - Claims "greatly improves cost efficiency" while keeping most of the strong model's performance. The abstract gives no numbers.
  - Source: [arXiv 2607.00053](https://arxiv.org/abs/2607.00053)
- Not Diamond: approximates Opus 4.8 Xhigh at 39% lower cost on Poly-SWE-Bench Verified, and 66% lower with open-weight models in the pool (vendor) — [Not Diamond blog](https://www.notdiamond.ai/blog/not-diamond-code-intelligent-model-routing-for-coding-agents)
- Fireworks FireRouter: $7.42 versus $15.81 per session compared with Opus 5 alone (-53%) over 2,334 production sessions (vendor; the page's title did not match its content) — [Fireworks](https://fireworks.ai/blog/the-frontier-isnt-a-model-its-a-router)
- Applied Compute trains an agentic router on SWE tasks (vendor) — [Applied Compute](https://www.appliedcompute.com/research/agentic-model-router)

### Inferences
- **Make routing a mid-trajectory escalation decision.** janetcode's model-slot step routing could become: start on a cheap model (DeepSeek flash or MiMo), then escalate to a strong model after k turns based on signals such as repeated failing tests, edit-apply failures, loops or no progress.
- **Router signals can be heuristics.** No learned value head is needed to start.
- **Escalation costs a full re-prefill.** Escalation is a cache miss on the new model (see Q1). Escalate once and stay escalated rather than ping-ponging between models. Alternatively, route only self-contained sub-tasks (summaries, spec review, commit messages) to other models in their own fresh contexts.

### Gaps
- I found no quantitative SWE-Router numbers (abstract only).
- I found no independent replication of the vendor routing savings.
