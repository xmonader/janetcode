# Changelog

User-visible changes. Commit-level detail is in `git log`; the reasoning
and measurements behind each round are in `docs/notes/history.md`.

## Unreleased (since v0.2.0, 2026-09-10)

### Security
- `--web` refuses cross-site and DNS-rebound requests. Before this, any open
  web page could run shell commands through `/api/prompt`. Upgrade if you use
  `--web`.
- Credentials are redacted once, where tool results are produced, so they
  never reach the model context, the session log or eval records. Environment
  variables are treated as credentials when a credential phrase appears
  anywhere in the name (e.g. `DEEPSEEK_API_KEY_PERSONAL`), and generic `sk-`
  keys are caught too.
- Provider credentials are scrubbed from every child process the project
  controls (hooks, verify, MCP, eval checks, the docker sandbox).
- Three command-classifier bypasses closed (`cd` segments, raw `/tmp` paths,
  unexpanded variables); `-o` output paths are now classified as writes.
- Provider HTTP clients refuse redirects to another origin and bound and
  redact error bodies before they reach the transcript.

### Web UI (`--web`)
- A browser session: live workspace pane (plan, files, jobs, subagents),
  embedded terminal, trajectory panel with timeline and search, structured
  `/perf` and `/usage` cards.
- Redesigned: one centred column, drawers, floating permission card, code
  highlighting with copy, mermaid diagrams, edit & resend, regenerate,
  find-in-conversation, jump-to-latest, phone layout.
- Starts without an API key and connects from the page; a busy port falls back
  to a random one; a stale page says the server was rebuilt.

### Terminal UI
- Full-palette themes (eight builtins plus your own) switchable with `/theme`.
- Live overlays for `/agents`, `/jobs`, `/todos`; `/trajectory` opens an
  interactive ledger browser.
- Mermaid diagrams drawn in the transcript.
- `shift+tab` cycles ask → plan → auto → yolo; `ctrl+r` or `/effort` changes
  reasoning effort live.
- The composer grows with its content; queue vs. steer is labelled.
- `/help` lists every built-in under its group.

### Sessions, safety and undo
- Auto mode, optional bash sandbox with named profiles (docker, seatbelt,
  Landlock) and a per-project `sandbox` config key.
- Permission prompts offer a ladder of rules, from narrow to broad, and rules
  can match a field (`tool(param:pattern)`). Paths whose effects outlive the
  session are protected.
- `/undo turn n` rewinds the work tree and the conversation; undo by
  description.
- `/cd` moves the session without restarting it; fork lineage is shown
  wherever sessions are listed.
- The plan is a file you can comment on, from either front end.

### Models and cost
- Model slots (main, cheap, strong, advisor, vision) set from `/model`,
  `/features` or `--set`. Step routing sends mechanical steps to a cheaper
  model. Every call records which slot served it and why, and `/routing`
  turns routing off for the session.
- Real per-model pricing from LiteLLM's table; `/usage` shows spend and
  cache-hit rate per model; `/perf` shows where the wall-clock time went.
- `--max-cost` is enforced in `--watch` too.

### Project tooling
- `--doctor --fix` applies the safe repairs it lists.
- `--init-project` writes a first instruction file from what the repository
  contains.
- MCP servers can be added and removed from a command or dialog, and
  `.mcp.json` is written for you.
- `/eval` keeps a real session as a regression task; `/recipe` replays a
  successful turn with no model call; `--proof` shows what demonstrated each
  change in a commit.
- `--what-if` reruns a recorded session against changed code, model or
  settings and compares the two runs; `--replay` reruns one at no token cost.
- Acceptance tests the model cannot weaken (`/accept`).

### Headless
- The `--output-format json` result carries routing evidence (`routes[]`).
- `--set key=value` applies any `/config` key for one run.

### Experimental (off by default)
- Optional Jev decision service (`decisions` config block): triage of
  verification failures and intake classification. Fails open. The
  measurements are in `docs/notes/jev.md`.
- Computer use through the Cua Driver MCP backend.

### CLI polish
- `--help` groups flags by task (Session, Headless, Permissions, Setup,
  Advanced, Eval) and uses the `--long` spelling the docs use.
- The missing-API-key error puts one fix per line, fastest first.

### Known limitations
- The Anthropic provider has been tested against a fake server only; the
  OpenAI-compatible path is tested live.
- Windows is compile-checked only.
