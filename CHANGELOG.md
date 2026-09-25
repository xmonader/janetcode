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

### Polish
CLI
- `--help` groups flags by task, uses the `--long` spelling, names what each
  flag takes (`--resume id`) and fits 80 columns.
- Usage mistakes exit 42 with one plain line: an unknown or malformed flag,
  an unknown `--resume` id or recipe, a `--from-turn` the recording lacks,
  a missing repo or commit for `--proof`, a second `--init`/`--init-project`.
- `--output-format json`/`stream-json` print a result object even when the
  run fails before it starts.
- Provider failures are explained (what failed, what to do); `--doctor`
  fails only the provider in use and lists optional features as info.
- `--recipe` runs with no API key; `--sessions` and counts read as words.
- The missing-API-key error puts one fix per line, fastest first.

Terminal UI
- Permission keys say what they do (`a this session`, `r save <rule>`); the
  approval line says "used 3 times in 2 files" instead of "3 refs".
- Reading a file that does not exist is an answer, not a red failure (web too).
- At 80 columns the status line keeps the cost, the connect list keeps the
  price, and the queue row keeps its keys; the command menu is aligned.
- `/undo` with nothing to undo is a note; titles cut short end in "…";
  a cancelled reply no longer ends on a bare `###`; light-theme borders
  reach 3:1.
- `/usage` before the first call says $0 instead of "unpriced".

Web UI
- The permission card asks "Edit a.go?" / "Run go test ./...?".
- Follows the OS light/dark setting on first visit; muted text reaches 4.5:1.
- Model menu: wider, named after the service (OpenRouter), current model
  first, "1M context".
- Header: "Effort: auto", no green dot without a model, no context meter
  before the first message; on a phone the model's own name and a dot for
  reconnecting, and drawers that start shut and never stack.
- Key failures offer a **Fix connection** button; plain labels throughout
  ("Scripts", "Show full reply", one set of permission-mode names).

### Robustness (lessons from diwan)
- Tool-call arguments with a dropped bracket or a trailing comma are mended;
  anything still not JSON is refused for that call and the model resends
  it, instead of the whole turn failing. A call cut off at the output
  limit is now reported as such and refused, never mended.
- Reasoning a model writes inline between `<think>` tags (Qwen3, R1
  distills on Ollama/vLLM/llama.cpp) arrives as thinking, not as the answer.
- A list or object sent as a string holding its JSON is accepted by every tool.
- Content streamed as a list of parts and usage sent as strings no longer
  read as a truncated stream.
- Fallback judges a provider error by its status and the server's whole
  text, not the display copy cut at 300 characters.
- Pruning drops the long strings from old calls' arguments (whole file
  bodies), keeping their paths; thinking tokens are shown in the usage line.

### Web, background runs and operations
- `--web` requires the access token in the URL it prints, on loopback
  too: other users and processes on the machine can reach 127.0.0.1
  (`JANETCODE_WEB_TOKEN` fixes it). A hidden tab raises a desktop
  notification when an approval is waiting.
- `--bg` no longer runs `--yolo` unless told to, and keeps the permission
  flags it is given (before, even `--bg --readonly` ran `--yolo`); what
  its mode asks about waits for `janetcode --answer
  <id> allow|session|deny`, `JANETCODE_NOTIFY` runs a command when one
  starts waiting, and `--sessions` shows each run as running, waiting,
  done or failed.
- `--backup <file>` / `--restore <file>` for the config and data
  directories; a restore saves what it replaces first.
- `--doctor --output-format json`; `--doctor --fix` makes a config file
  other users can read 0600.
- `/mcp search <words>` prints the `/mcp add` line for servers in the
  official MCP registry; it installs nothing. Results are ranked (a local
  package with a repository first) and a publisher's near-duplicates fold
  into one line.
- `make dist` builds release binaries with `SHA256SUMS`; a `Dockerfile`
  and a tag-triggered release workflow (binaries and a GHCR image).
- CI runs the markdown XSS corpus, the contrast checks and three browser
  suites, one of them real turns against a scripted model (`make ui-turn`).

### First week
- Install without Go: `install.sh` downloads the release binary for the
  machine and checks it against `SHA256SUMS`; `janetcode --update` does
  the same in place.
- `docs/getting-started.md`: the first ten minutes, from install to a
  reviewed change.
- `--help` is one screen of everyday flags (`--help all` for all 76);
  `/help` is one screen of everyday keys and commands (`/help all`).
- `/permissions` opens with one plain summary of what runs, what asks
  first and what never runs, in the terminal and the browser alike.
- The first-run banner says `/connect` rather than pointing at a closed
  dialog, and wraps between its items with a hanging indent.
- `--plain` (or `JANETCODE_PLAIN=1`, on by itself when `TERM=linux`)
  draws the terminal interface in ASCII with a still spinner.
- The web's folded tool bursts colour only their "(n failed)" count red.
- Fixed: the web's paperclip file picker did nothing (a second
  `uploadFiles` shadowed the first and called `forEach` on a FileList).
- A long session no longer slows every frame: the transcript's viewport
  stopped measuring every line's width on each change (39 ms to 8 ms a
  frame at 20,000 lines). Startup, keystroke, new-line, resize and memory
  budgets are now tests.
- `app.js` is split: renderers, the trajectory view, the terminal tab and
  the connect dialog are their own files; every browser suite fails on
  any uncaught exception in the page.

### Known limitations
- The Anthropic provider has been tested against a fake server only; the
  OpenAI-compatible path is tested live.
- Windows is compile-checked only.
