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
- Last release tag: v0.2.0; ~1040 commits since.
- Polish/look passes 2026-09-24/25: CHANGELOG "Polish", "Robustness", "Web,
  background runs and operations", "First week", "Look and feel". Brand:
  iris accent + iris→aqua gradient (agent's pick; alternatives offered).
  Checks: `make ui-test ui-turn ui-a11y ui-visual` (shot.mjs pins
  `/api/suggest`). TUI review: pty frames rendered to PNG. Not taken from
  diwan: native Gemini client, schema-enum drift test. Release pipeline
  rehearsed locally; install.sh/--update need the GitHub repo public.
- deer-flow/trae-agent reviewed 2026-09-25: took skill scan + open-plan gate.
  Already had: loops, truncation refusal, searchable compaction archive.
- Qwen/Claude parity 2026-09-25 (vs qwen-code ffea2d0 on herobox): 14 hook
  events + Claude/Qwen hook configs read as-is; `status_line` command
  (TUI only; Qwen's preset mode refused); `vision_model` bridge (images
  described for a text-only model, cached per image). All live-verified
  on Dashscope. Not done: Qwen's full-turn switch to an agent-capable
  vision model; `tool_name`/`tool_input` stay janetcode's own.

## Next
- Live-verify the Anthropic path (held by user 2026-09-24).
- Cut v0.3.0: push, make the repo public, push the tag (release.yml does
  the rest). Needs the user's go-ahead.
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
- Open UI questions: shared view model for TUI/web. app.js is still ~2,000
  lines: the transcript, composer, sessions rail and workspace pane share
  mutable state and were left together.
- `update.Newer` compares only vX.Y.Z: a v0.3.0-rc build does not update to
  v0.3.0.

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
- Usage errors are `headless.BadInput` (exit 42) with one plain line; a
  message never names a TUI command from the CLI (2026-09-25).
- A missing path on read_file/list_dir is an answer, decided once in
  `present.EndOf` for TUI, web and replayed sessions (2026-09-25).
- Malformed tool arguments are mended or wrapped (`llm.InvalidArguments`)
  in the provider and refused per call in the agent, never a stream error;
  the Anthropic path is untouched (2026-09-25).
- `--web` off loopback is token-gated; loopback stays origin/Host-gated only.
  `--bg` status and approvals are sidecars beside the session file
  (`.bg.json`, `.bg-answer.json`), polled, no daemon (2026-09-25).
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
