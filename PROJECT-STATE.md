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
- Last release tag: v0.2.0; ~1970 commits since.
- Rounds since 2026-09-24 (user expectations, qwengo parity 1-2, novel and
  wild ideas, t3code 2026-11-16): docs/notes/history.md and the CHANGELOG
  sections of the same names. Test broker: ai02
  (OPENAI_BASE_URL=https://api.ai02.ourhero.ai/v1). UI checks: `make
  ui-test ui-turn ui-a11y ui-visual` (UI_CDP_PORT/UI_VISUAL_CDP_PORT for
  concurrent runs). install.sh/--update need the GitHub repo public.

## Next
- Live-verify against api.anthropic.com itself (needs an sk-ant key).
- Cut v0.3.0: push, make the repo public, push the tag (release.yml does
  the rest). Needs the user's go-ahead.
- GitHub issues: #6 and #7 closed (2026-11-16). #1-#3, #5, #8-#12 stay open:
  checked against their acceptance criteria, parts are missing (no file
  admission mode #2, no `--doctor security` #10, no extension doctor #8, no
  evidence-status taxonomy #5, no observers #11, no offline audit replay #12,
  no dry-run cost estimate #9, partial checkpoints #1 and memory #3).
- t3code round, live checks left: phone approval end to end over tailscale serve (machine needs
  `doas tailscale up`, Tailscale 1.104.7 installed); a real `op read`; the
  ntfy hook against a real topic.

## Known Gaps (verify before relying on)
- Eval Wilson CI pools repeated runs as iid.
- Mid-turn Jev consumer rejected twice on measurements (docs/notes/jev.md);
  revisit only with a harness-side "did the state change" check.
- Open UI questions: shared view model for TUI/web. app.js was split from
  2,821 to ~1,190 lines: one file per area (find, settings, panels, notify,
  switchmenu, modemenu, sessionsrail, workspacepane, approvals, composer,
  palette, guest) over a shared `JanetState` (state.js holds the state map).
  What stays in app.js is the transcript render, event stream and header
  chips: render() writes the DOM caches (nodes, open* sets) that the event
  stream, find and markdown export read, so they cannot move without a
  render/stream interface.
- Left open from the deep review: seatbelt mach-lookup allowlist is
  untested on a Mac; the `workspace` sandbox keeps unix sockets (use
  `strict`); `settingsapply` can write the decisions map a web request's
  config copy shares.
- `/pr` and `/pr-comments` verified live against github.com 2026-11-16
  (docs/notes/github-live.md). The Action on a hosted runner is not: the
  account's Actions jobs do not start (billing) and the repo has no secret.
- t3code round gaps: secrets are memory-only (lost on resume); ssh remote
  secrets sit on the remote disk (0600) between two ssh calls, and an
  abandoned staging file is not swept; codex delegate cost is unknown
  (tokens only); op:// values cached 5 min; --bg runs get no deep link.
- Landlock egress is by port (a remote host on the proxy's port is reachable);
  only used when bwrap (netns + unix-socket bridge) and docker (isolated net +
  sidecar) are both unavailable. Docker sidecar needs busybox nc in the image.

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
- Lean execution round 2026-10-08 (docs/benchmarks/lean-ab, ideas from
  hero_agent's effbench): small repo inlined in turn 1, then_run, background
  LSP checks, cache-stable tool loading, headless memory opt-in, cut-off
  replies sent back. MiMo V2.6 Flash, Novita-only: calls −41%, wall −28%,
  cost −12%, passes level. Inlining carries most of it. The broker mixes
  upstreams (GMICloud writes ~3x less): compare on one `served.provider`.
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
