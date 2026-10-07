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
  and others; the Anthropic path through the ai02 broker's Messages API
  (2026-09-30, docs/notes/anthropic-live.md), not api.anthropic.com itself.
- Windows: run under wine (tests + a live headless turn, docs/notes/
  windows-wine.md); PowerShell paths need a real Windows run (CI job).
- User-expectation checklist 2026-09-30 (docs/notes/user-expectations.md):
  every gap except subscription login closed by 11+3 parallel forks, merged
  on master; CHANGELOG "User-expectation gaps". Test broker: ai02
  (OPENAI_BASE_URL=https://api.ai02.ourhero.ai/v1), cheap models
  deepseek/deepseek-v4.1-flash, xiaomi/mimo-v2.6-flash, qwen/qwen3.8-27b.
- Last release tag: v0.2.0; ~1040 commits since.
- Earlier rounds (polish/look 09-24/25, deer-flow/trae review, Qwen/Claude
  parity 09-25): CHANGELOG sections of the same names and
  docs/notes/history.md. UI checks: `make ui-test ui-turn ui-a11y
  ui-visual`. install.sh/--update need the GitHub repo public.

- qwengo parity 2026-10-06: `--json-schema`, `--input-format stream-json`,
  native Gemini + OpenAI Responses providers (shared loop in
  `internal/llm/httpstream`), `/commit` + `/pr` (`internal/gitship`).
  Live-verified: json-schema, stream input, Responses (encrypted
  reasoning replay) and /commit drafting via OpenRouter. Native Gemini is
  verified only against a fake server (no Gemini key here). Not taken from
  qwengo: extensions manager, teams/arena tools, ~40 themes, vim input.
- qwengo parity round 2 (2026-10-06): --max-tool-calls (57),
  --max-wall-time (56), --core-tools/--exclude-tools (registry gate),
  .aiignore (internal/ignore), --safe-mode, Tavily/Serper/Exa/SearXNG
  search, bwrap sandbox (preferred on Linux), --debug, --list-models,
  update_check notice, ctrl+x $EDITOR, drag-select + copy (OSC 52
  fallback). Live: Serper, Exa, list-models; bwrap/editor/drag via pty.
  Not live: Tavily, SearXNG (no key/instance).

## Next
- Live-verify against api.anthropic.com itself (needs an sk-ant key).
- Cut v0.3.0: push, make the repo public, push the tag (release.yml does
  the rest). Needs the user's go-ahead.
- GitHub issues #1-#3, #5-#12 are implemented on master (2026-09-30);
  closing them on GitHub is outward-facing, so it waits for the user.

## Known Gaps (verify before relying on)
- Eval Wilson CI pools repeated runs as iid.
- Mid-turn Jev consumer rejected twice on measurements (docs/notes/jev.md);
  revisit only with a harness-side "did the state change" check.
- Open UI questions: shared view model for TUI/web. app.js is still ~2,000
  lines: the transcript, composer, sessions rail and workspace pane share
  mutable state and were left together.
- Left open from the deep review: seatbelt mach-lookup allowlist is
  untested on a Mac; the `workspace` sandbox keeps unix sockets (use
  `strict`); `settingsapply` can write the decisions map a web request's
  config copy shares.
- GitHub Action, `/pr` and `/pr-comments` tested against a stub `gh` and
  (action) under nektos/act, never against github.com (needs a push).
- The Landlock egress rule is by port: a remote host on the proxy's port is
  reachable. Docker egress: host services on 0.0.0.0 reachable.

## Key Decisions
- ctrl+r is reverse prompt search (shell convention); effort cycling moved to
  alt+e. Every prompt key goes through internal/keymap (2026-09-30).
- A project's skills/agents/commands/rules/recipes need project trust
  (`instructions`); hooks/MCP/verify/allow rules/recipes need `full`.
  JANETCODE.md/AGENTS.md/CLAUDE.md are always read (2026-09-30).
- 2026-10-02 orient/baseline A/B (docs/benchmarks/orient-baseline):
  `verify_baseline` on by default (held-out controls 0/4 → 5/5); orient
  advice stays off — no headroom on hidden/gated/hard for DeepSeek
  V4.1 Flash, retest on a set the executor fails. Live evals: use the
  broker's self-hosted routes (`deepseek-ai/DeepSeek-V4.1-Flash`,
  `XiaomiMiMo/MiMo-V2.6-Flash-RL`, `Qwen/Qwen3.8-27B-FP8`), one task at
  a time; ~45% of runs drop as INFRA (504 over mycelium).
- A compiled agent loop (`run_plan`, explore-then-compile) was measured and
  rejected 2026-10-02 (docs/benchmarks/jit): models barely use it and it
  saves nothing; the code stays on branch development_jit. Before any live
  eval, check the serving provider: the ai02 broker silently served Kimi
  for every model name on 2026-10-01.
- The Go SDK drives the binary over ACP rather than importing the builder
  (2026-09-30).
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
