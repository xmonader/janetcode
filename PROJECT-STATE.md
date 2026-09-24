# janetcode — project state

Checkpoint, not a journal: keep under ~100 lines. Per-round detail (commits,
tables, evidence) lives in `docs/notes/history.md`; experiment write-ups in
`docs/notes/*.md` and `docs/benchmarks/`.

## Current State
- Done: Go terminal coding agent (TUI, `--web`, headless `-p`, ACP). Anthropic
  (official SDK) + hand-rolled OpenAI-compatible providers, /connect presets,
  sessions/fork/rewind, compaction, permissions + verification gates, LSP, MCP
  (incl. OAuth), memory, skills, repo map, provenance/proof notes, evals,
  model slots + step routing with per-call evidence, optional Jev decision
  service (fail-open).
- Verified live: Dashscope `qwen3.7-plus`, OpenRouter `deepseek/deepseek-v4-flash`
  and others. Anthropic path verified only against a fake server.
- Windows: compile-checked (`make cross`), never run.
- Last release tag: v0.2.0; ~880 commits since.
- Polish pass 2026-09-24: no-key error is one fix per line; `--help` grouped
  by task (Session / Headless / Permissions / Setup / Advanced / Eval) and
  spelled `--long`; `/help` files every built-in (11 were listed as custom);
  this file trimmed, history moved to `docs/notes/history.md`.

## Next
- Live-verify the Anthropic path (held by user 2026-09-24).
- Cut a release with a CHANGELOG.
- Open GitHub issues #1-#3, #5-#12 (trust, permissions, dry-run, egress,
  session leases, audit/replay).

## Known Gaps (verify before relying on)
- `fetch_url` and remote-MCP private-target guards are literal-IP only (no
  DNS-resolution check) — open since 2026-09-17.
- `fallback.go` classifies provider errors by free-text regex.
- Checkpoint file copies unbounded for single >16 MB changes; non-git undo
  cannot see post-turn edits.
- Eval Wilson CI pools repeated runs as iid.
- Mid-turn Jev consumer rejected twice on measurements (docs/notes/jev.md);
  revisit only with a harness-side "did the state change" check.
- Open UI questions: shared view model for TUI/web; visual-diff gate.

## Key Decisions
- Go + bubbletea; product name is janetcode, not the Janet language (2026-09-07).
- Anthropic via official SDK; OpenAI-compatible hand-rolled to tolerate dialects
  (Qwen, DeepSeek, vLLM, Ollama, OpenRouter) and learn quirks from 400s (2026-09-07).
- History is append-only (preserved thinking rejects edited history); compaction
  starts a fresh history keeping a verbatim tail (≤6 msgs, ≤12k tokens).
  `Agent.Prune` rewrites only old tool_result text, never thinking blocks (2026-09-08).
- A turn's tool results go back in one user message; history stays
  provider-valid on every return path, including cancellation (2026-09-07).
- Sessions rotate to a new file when history shrinks; pre-compaction
  transcripts are never overwritten (2026-09-07).
- Credentials are redacted once at `Agent.runSafely`, the point every tool
  result passes, not per sink (2026-09-22; `Off["scrub"]` disables).
- Credential env vars matched by whole `_`-separated words anywhere in the name;
  `GITHUB_TOKEN`, `NPM_TOKEN`, `CARGO_REGISTRY_TOKEN`, `AWS_ACCESS_KEY_ID` kept
  because their CLIs need them (2026-09-22).
- Auto-approve never skips the command guard (2026-09-22).
- New flags land in `--help` "Advanced" unless filed in `helpGroups`
  (cmd/janetcode/flags.go); new slash commands must be filed in the TUI
  `helpGroups` (a test enforces it) (2026-09-24).

## Architecture Notes
- `internal/llm` is the provider-neutral core: Message/Block, ToolDef, Request,
  Event, `Provider.Stream(ctx, req, emit)` → Response.
- `internal/agent.Agent.Run` is the loop; `Approver` is the permission hook.
- TUI never touches agent state mid-turn: the agent goroutine pushes events into
  `eventQueue` (drained on `flushMsg`) and sends `agentDoneMsg`/`permissionMsg`/
  `askMsg` through `model.send`.
- Terminal background is detected once before start; creating glamour renderers
  with AutoStyle later re-queries the terminal and eats keystrokes.
- Markdown renders per blank-line block with a per-entry cache (glamour ≈0.9 s per
  20 KB); guard: `TestStreamingLargeAnswerStaysResponsive`.
- Exit codes live in one table, `internal/headless` `exitRules` (41 = auth).
- Testing the TUI under a real pty: answer OSC 11 and DSR queries or
  bubbletea/termenv blocks for 5 s.
