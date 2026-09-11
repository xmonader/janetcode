# qwen-code, deeply: what to adopt

Two Opus agents read qwen-code (~/xmoncode/qwen-code, TypeScript, packages/core
and packages/cli) on 2026-09-11, one on the agent core, one on tools and
integrations, each against what janetcode already has and with the standing
rule that anything growing the per-call prefix needs a strong case:
janetcode's prefix is about 1.8k tokens, qwen-code's about 15k eager and
23k once deferred tools preload, and the eleven-model benchmark measured
5–9x fewer tokens for the same tasks. File references are qwen-code's.

## Where janetcode is already ahead (keep)

- The prompt: one 4.5 KB builder with an explicit cache split, against a
  22 KB base plus a session-start folder dump and an 8 KB skill listing.
- Compaction: prune first, verbatim tail, typed working-memory record,
  archive with `expand_output` and NEED recovery of any fact, against
  summarise-everything with five files re-read from disk as the recovery.
- Verdicts computed from events, against an LLM goal judge.
- Snapshots as hidden git commits, against 100 file copies per session.
- Key rotation with per-key circuit breakers; qwen-code has nothing like it.
- The eval instrument (Wilson intervals, bootstrap compare, ablations,
  mutation gate, cross-harness benchmark); qwen-code has no evals.
- No telemetry. qwen-code posts ~45 event types to Alibaba RUM, on by default.

## Adopt, ranked across both reports

1. **`--output-format json|stream-json` and typed exit codes for `-p`**
   (`cli/src/nonInteractive/types.ts:152-190`, `utils/errors.ts:311-365`).
   A result envelope with usage, turns and denials; exit 41 auth, 42
   input, 52 config, 53 turn limit, 55 budget, 130 cancel. Our own
   benchmark drives other CLIs this way; ours prints text. ~2 days.
2. **Stream watchdog**: a resettable idle timeout (240s) and a
   non-resettable lifetime cap (900s) (`core/stream-guards.ts:174`). A hung
   SSE stream today has no bound. ~1 day.
3. **Text-to-tool-call recovery** (`core/xml-tool-call-fallback.ts:9-48`):
   a small model that emits `<invoke name=…>` as prose gets its call
   executed instead of a wasted turn. Measurable on the hard set. ~1 day.
4. **Model fallback chain** (`config/config.ts:1521`, `llm-chat.ts:4294`):
   an ordered list, capped at 3, with a phase notice; the DashScope quota
   429 that lost a benchmark run is the case. ~1 day.
5. **Grep, diff and test exiting 1 is not an error** (`tools/shell.ts:428-443`).
   We report every non-zero exit as failure; it costs turns on every run.
   ~1 hour.
6. **Loop taxonomy** (`services/loopDetectionService.ts:41-169`):
   alternating A-B cycles, read_file thrash (8 in 15), repeated identical
   assistant text, a 100-calls-per-turn cap. We catch identical repeats
   only. ~1 day.
7. **Encoding, BOM and line-ending round-trip** in read/write/edit
   (`utils/fileUtils.ts:111-360`, `write-file.ts:526-534`). We read and
   write UTF-8 bytes; CRLF and GBK repos get rewritten. ~2 days.
8. **Conditional path-scoped rules** (`config/rulesDiscovery.ts:197-360`):
   `.janetcode/rules/*.md` with `paths:` globs, injected lazily per tool
   call at most once per session. The one way to grow instructions
   without growing the prefix. ~1–2 days.
9. **Self-modification surfaces always ask**, even under `--yolo` and
   allow rules, including through the shell (config, `.mcp.json`, hooks,
   skills, permission rules; `docs/users/features/auto-mode.md:17-27`).
   Narrow virtual-file-op detection for those paths only. ~2 days.
10. **Structured prior-read failures** (`tools/priorReadEnforcement.ts`):
    a distinct message per cause (disappeared, stale, directory, never
    read) with verb-specific guidance. Ours is one message. ~1 day.
11. **Unicode-equivalence fold in edit matching** (`utils/editHelper.ts:20-63`)
    and a near-miss diff on zero matches; neither harness does the
    latter. ~1 day.
12. **`failed:true` in checkpoint entries** (`services/fileHistoryService.ts:32-39`):
    a capture that failed must not restore stale content. ~half a day.
13. **System-prompt override** (`QWEN_SYSTEM_MD`, `--append-system-prompt`;
    `prompts.ts:506-562`): prompt A/B as a flag against the eval comparer.
    ~2 hours.
14. **Expand stubs carry a sha256 of the pre-truncation output**, and
    previews escape tag-shaped text (`tools/truncation.ts:24-33,397-401`),
    so loop detection works on large results and no result can forge a
    delimiter. ~2 hours.
15. **Subagents**: `bubble` approval (a needed confirmation queues to the
    parent UI instead of auto-deny), `disallowedTools`, per-agent turn and
    time budgets, worktree-registry validation before pinning
    (`subagents/types.ts:79-110`, `agents/worktree-pin.ts:38-45`). ~2 days.
16. **Hierarchical instruction discovery** (parents' `AGENTS.md`) and
    `@file` imports with a depth cap and root confinement
    (`memory/memoryDiscovery.ts:95-214`, `utils/memoryImportProcessor.ts:210-476`).
    ~1.5 days.
17. **Web hardening** (`web-fetch.ts:145-166,286-302,394-397`): no
    cross-host redirect follow, never cache an https→http fallback, GitHub
    blob→raw before the permission check, magic-byte sniff. ~1 day.
18. **MCP**: per-call nonce framing of resource content, no auto-replay of
    calls that may have had side effects (`mcp-resource-content.ts:56-62`,
    `mcp-tool.ts:507`). ~1 day.
19. **Batch-reveal deferred tools within a token budget** rather than one
    at a time, since each reveal breaks the cache prefix
    (`tool-registry.ts:956-1000`). ~half a day.
20. **Small fixes**: 800-char cap on todo re-injection and `blockedBy`
    dependencies (`tools/todoWrite.ts:27,42-71`); tool-call ID
    normalisation (`core/toolCallIdUtils.ts:11`); honour `Retry-After`
    only on 429/503 and do not retry 500; an image-token term in the
    estimator; rank-based reasoning-effort clamping; a secret scan before
    writing a memory entry; foreground-to-background promotion of a
    running command; `sed -i` shown as a diff; hook settings a workspace
    scope cannot widen. ~3 days as a batch.

## Do not copy

- The 22 KB base prompt, the folder dump, the 8 KB skill listing, and
  1,000–3,600-token tool descriptions. That is the 5–9x.
- The `agent` tool rewriting its own description at runtime (breaks the
  cache prefix).
- Summarise-everything compaction; microcompaction as a second mechanism
  (only its cache-preserving low watermark is worth checking against
  `pruneTo(limit/2)`).
- The LLM goal judge and provenance-tagged goal evidence.
- File-copy checkpointing; the 2,700-line writer lease (the EOF metadata
  re-anchoring is separable and worth ~1 day for O(tail) listing).
- Speculative execution of a predicted next prompt on an overlay FS.
- Arena, teams, mailbox, tmux/iTerm backends, cron, IPC, extension
  marketplace, channels, CUA drivers, voice.
- 24 hook events and LLM `prompt` hooks; the full 2,365-line shell
  semantics model; MCP connection pooling; Docker/seatbelt sandboxing for
  now; the message bus that auto-approves everything.
- Default-on usage telemetry.
