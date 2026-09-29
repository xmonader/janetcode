# janetcode

A terminal coding agent in a single Go binary. It reads and edits code, runs
commands, and iterates on the result, with every mutating action shown to you
as a diff before it happens. Works with Anthropic's Messages API and any
OpenAI-compatible endpoint (OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM,
Ollama).

New here? [Getting started](docs/getting-started.md) is ten minutes from
install to a reviewed change.

## Install

On Linux or macOS, from the latest release (checked against its
`SHA256SUMS`, installed to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/xmonader/janetcode/master/install.sh | sh
janetcode --update  # later: the newest release, checked the same way
```

From source (Go 1.25+):

```sh
git clone https://github.com/xmonader/janetcode
cd janetcode
make install        # puts janetcode on your $GOPATH/bin
```

Tagged releases after v0.2.0 also publish prebuilt binaries for Linux,
macOS and Windows with a `SHA256SUMS` file on the GitHub release page, and
an image, `ghcr.io/xmonader/janetcode`:

```sh
docker run -it --rm --user "$(id -u):$(id -g)" -v "$PWD:/work" \
  -e OPENAI_API_KEY ghcr.io/xmonader/janetcode
```

The `Dockerfile` header shows the `--web` variant. `make dist` builds the
release binaries locally.

Linux and macOS are where janetcode is developed and tested. Windows
support is best effort: `GOOS=windows go build ./...` (`make cross`) is
kept green and the shell paths are switched by OS, so `bash`, hooks,
verification commands, `--watch`/`--until` checks and background jobs
run through PowerShell (`pwsh` if installed, else Windows PowerShell)
with the command passed as `-EncodedCommand` to avoid quoting rules, and
cancelled commands are killed with `taskkill /T`. None of that has been
exercised on a real Windows machine yet, and the command safety
classifier still reasons about POSIX shell syntax, so treat it as
untested. The cheapest way to use janetcode on Windows today is WSL.

## Quick start

```sh
export ANTHROPIC_API_KEY=sk-ant-...
cd your/project
janetcode
```

Or with an OpenAI-compatible backend. Tested live against Alibaba's coding
endpoint and OpenRouter:

```sh
# Alibaba Model Studio (Qwen coding plan)
export OPENAI_API_KEY=sk-...
export OPENAI_BASE_URL=https://coding-intl.dashscope.aliyuncs.com/v1
janetcode --model qwen3.7-plus

# OpenRouter
export OPENAI_API_KEY=sk-or-...
export OPENAI_BASE_URL=https://openrouter.ai/api/v1
janetcode --model deepseek/deepseek-v4-flash

# Ollama (no key needed for a local server)
export OPENAI_BASE_URL=http://localhost:11434/v1
janetcode --model qwen3-coder
```

Or skip the environment and run `/connect` inside the TUI: pick a preset
(Anthropic, OpenAI, OpenRouter, Alibaba Qwen, DeepSeek, Moonshot, Zhipu,
MiniMax, Gemini, xAI, Mistral, Groq, Cerebras, Together, Fireworks, Ollama,
LM Studio) or a custom OpenAI- or Anthropic-compatible URL, paste one or
more API keys, choose a model from the server's list, and the choice is
saved to the config file. Several keys for one provider are rotated
automatically when one is rate limited or rejected.

The provider is inferred from whichever key is set (`--provider` overrides
it, and accepts any configured or preset name). Put `model` and the keys in
the config file (see below), or export `JANETCODE_MODEL`, to skip the flag.
A prompt given as an argument starts the session with it:

```sh
janetcode "explain how requests flow through this service"
```

The same session in a browser, on loopback:

```sh
janetcode --web
# janetcode serving http://127.0.0.1:8787/?token=…   (open this link)
```

One-shot, scriptable mode:

```sh
janetcode -p "add a --verbose flag to cmd/server" --yolo
```

Assistant text goes to stdout, tool activity to stderr. Without `--yolo`,
mutating tools prompt on the terminal, or are denied when stdin is not a TTY.

`--until "<command>"` keeps the run going until a shell command exits 0:
after each turn the command runs in the working directory and its output
goes back to the model on failure, at most five rounds. The verdict is the
exit code, never the model's opinion.

```sh
janetcode -p "make the flaky TestSync deterministic" --yolo --until "go test -count=3 ./internal/sync/"
```

`--watch "<command>"` is the reverse: janetcode stays running, re-runs the
command whenever files under the directory change (you edit in your own
editor), and when it fails sends the output to the model to fix. A pass
prints `✓`; five failed fixes in a row make it wait for your next change.
`-p` adds standing instructions for the fixes; `--yolo` lets it edit
without prompts, which is what you want here:

```sh
janetcode --watch "go build ./... && go test ./..." --yolo -p "keep the tests green; never edit *_test.go"
```

`janetcode --doctor` checks the config file, every configured provider's
keys, a live call to the selected model, the sessions directory, git, local
tools, MCP servers, the project's hooks, verification commands and
permission rules (and whether they are trusted), skills, the memory store
and which language servers are installed. It exits non-zero when
something is broken; a missing optional feature is only a warning.
`/stats` in the TUI shows the session's turns, model calls and wait time,
tool calls per tool with failures, tokens, cache hit rate and cost.

## What it can do

| Tool | Purpose | Asks permission |
|------|---------|-----------------|
| `read_file` | Read a file with line numbers, windowed | no |
| `list_dir` | List a directory | no |
| `glob` | Find files by pattern (`**/*.go`) | no |
| `grep` | Search file contents with a regex | no |
| `edit_file` | Replace text in a file, located by exact string, line range or content anchor; several edits in one call | yes, shows a diff |
| `write_file` | Create or overwrite a file | yes, shows a diff |
| `bash` | Run a shell command with a timeout | yes |
| `fetch_url` | Fetch a web page or API response as text | yes |
| `repo_map` | Rank files for a query, or a symbol's blast radius (definers and users) | no |
| `web_search` | Search the web (DuckDuckGo, or Brave Search with a key) for titles, URLs and snippets | yes |
| `task` | Delegate a job to a subagent with its own context, optionally on another model; `agent: explore` is a built-in read-only explorer; several run in parallel | no |
| `symbols` | Ask the language server where a name is defined, who uses it, its type, what a file declares | no |
| `expand_output` | Read back tool output and conversation that compaction archived, by search or by line | no |
| `execute_code` | Run a Python script that calls these tools, so a chain of calls costs one turn and their output stays out of the context | yes, shows the code |
| `run_code` | Where no Python is available (or `JANETCODE_OFF=script`), stands in for `execute_code`: a fixed JSON list of real tool calls in one turn (approvals and guards still apply) | yes |
| `workflow` | Start real `task` children from a short script and return their reports | no |
| `terminal` | Persistent shell session: cwd and environment survive across sends | yes |
| `todo` | Keep the plan for a multi-step task and mark steps off; triggered by a countable rule (three or more distinct steps) after [a subjective one was never reached for](docs/benchmarks/harnesses/session/README.md) | no |
| `ask` | Put one or more labeled questions in front of you as tabs; Other is free text | no |
| `inspect` | Look at the session's own state: context, archive, verdicts, config, servers | no |
| `diagnostics` | Compile and type errors from the language server, for a file or the project | no |
| `job` | List, read, wait on, kill, or write stdin to a background job | kill and stdin only |
| `memory` | Remember a fact about the project, or search what is remembered | no |
| `skill` | Load a skill's instructions on demand | no |
| `goal_status` | Mark the session goal complete or blocked | no |
| `tool_search` | Find a tool by what it does, when many are loaded | no |

All paths are confined to the working directory, symlinks included.
Read-only tools run concurrently; mutating tools run one at a time, in order.

**Experimental:** `route_cheap` (off by default) sends a turn's mechanical
steps — reads, greps, a check that already passed — to a cheaper model
while planning and edits stay on yours; see [Configuration](docs/manual/configuration.md#step-routing-experimental)
and "Measuring step routing" in [Evals](docs/manual/evals.md) before turning it on.

## Using it

The transcript streams the model's markdown as it arrives, shows each tool
call on one line with what it did and how long it took, and renders every
edit as a diff with line numbers, elided gaps and the changed tokens
picked out. The status line shows what the agent is doing, the
elapsed time, and a gauge of how full the context window is. The input box
grows with your text.

```
enter            send        ctrl+j / alt+enter   newline
up / down        recall earlier prompts          tab   complete a /command
                 or its argument (/resume, /model, /forget, /undo, /open, /diff);
                 a name matches on its letters in order, so /rsm finds /resume
esc              cancel the running turn (or clear the input)
ctrl+o           expand the last turn's output   ctrl+l   redraw
                 (again folds it; click a tool
                 line or thought to fold one;
                 /verbose does every turn)
pgup / pgdn      scroll (mouse wheel works)
ctrl+c           cancel the turn; when idle, press twice to quit
```

While the window is unfocused, a permission prompt or a finished turn
raises a desktop notification (`JANETCODE_NOTIFY=0` turns that off); the
tab title carries the session title, and OSC 133 marks let the terminal's
own prev/next-prompt keys jump between turns.

`↑` recalls prompts from earlier sessions in the same directory (`esc`
puts your draft back), and a `!` command comes back as one.

A paste of more than ten lines becomes a chip — `[paste #1 +123 lines]` —
that deletes as one unit and is expanded again when you send.

At a permission prompt: `y` allow once, `a` allow that tool for the session,
`n` deny and type why (the reason goes to the model, enter skips it), `esc`
deny silently, `c` cancel the whole turn. Destructive shell commands (recursive deletes, force pushes,
sudo, piping downloads into a shell) are flagged in the prompt.

Typing while the model is working queues the message; it is sent as soon as
the turn ends. `ctrl+s` instead hands what you typed to the turn that is
running — the model reads it before its next step. `↑` with an empty prompt
takes the last queued message back to edit, and cancelling a turn hands all
queued text back to the input, joined by blank lines.

```
@path            attach a file, an image (png/jpg/gif/webp), or a directory listing; tab completes
ctrl+v           attach the image in the clipboard (wl-paste or xclip on Linux, pngpaste on macOS)
!command         run a shell command here; its output is shown and shared with the model
/help      /help <topic> (search every feature and the manual)   /quit      /clear (new session)
/yolo (toggle auto-approve)   /auto (edits and known-safe reads)   /edits (edits go through, commands ask)   /hints [on | off]
/why [n] (explain the last thing the harness did on its own)   /budget <usd> (stop at a spending ceiling)
/plan <request> (plan mode: explore, write a plan; /plan approve executes it)
/accept <request> (write acceptance tests first; /accept approve locks them as a gate)
/readonly  (toggle read-only: the model can inspect but change nothing)
/sandbox [on|off]  /review [focus]  /loop [duration] <prompt>
/connect   /model     /compact (summarise the context to free the window)
/usage     /stats (turns, model calls, tool calls per tool, tokens, cost)
/resume (searchable list of earlier sessions; /resume <n or id> reopens one directly)   /verbose   /settle (list it last)
/undo [preview | about <description> | apply]  /redo  /find X    /cost      /mcp [trust | auth <server> | logout <server>]
/fork [title]  /title <text>  /history <words> (search every saved session)
/open path[:line] (view a file inline)   /diff [path | revert path:n] (hunk review)
/context   (what fills the window)   /pin <note or @file>   /unpin <n>
/export [file.md] (write the conversation as a Markdown transcript)
/eval keep [name] [--check cmd] (turn this session into a regression eval task)
/proof [commit] (which session, turn and intent wrote each file of a commit, and what showed it worked)
/recipe [list | show <name> | save <name> | run <name>] (replay a turn's tool calls with no model call)
```

`ctrl+c` cancels a running turn, or clears typed text; when idle it asks
for a second press before quitting, so a stray one never closes a session.

## Manual

The full manual lives in [`docs/manual/`](docs/manual/README.md):

- [The interface](docs/manual/interface.md) — keys, commands, permission rules, and every feature below
- [Configuration](docs/manual/configuration.md) — the config file, providers, prices (config, provider listing, or LiteLLM's rate table), routes, project instructions
- [Sessions](docs/manual/sessions.md) — resume, fork, search, compaction, snapshots
- [Evals](docs/manual/evals.md) — the task format, runs, comparisons, ablations
- [Command-line reference](docs/manual/cli.md) — every flag, environment variable and exit code, and the `--output-format json` result envelope
- [Editor integration](docs/manual/editor.md) — the Agent Client Protocol

Features, each a section of the interface page:

- [Permission rules](docs/manual/interface.md#permission-rules) — allow once, for the session, or by a saved rule such as `bash(go test *)`; a per-project ledger notices what you keep approving and proposes saving it, never saving one itself
- [Memory](docs/manual/interface.md#memory) — facts and preferences kept across sessions, one markdown file each, recalled per turn
- [Skills](docs/manual/interface.md#skills) — markdown instructions for one kind of task, attached when the prompt matches or run as `/name`
- [Background jobs](docs/manual/interface.md#background-jobs) — long commands run detached; the model is told when they finish
- [Editing subagents in a worktree](docs/manual/interface.md#editing-subagents-in-a-worktree) — `task` with `edit: true` works in an isolated worktree and returns a patch
- [A whole session in a worktree](docs/manual/interface.md#a-whole-session-in-a-worktree) — `--worktree <branch>` keeps the session off your checkout
- [Sending a job to another model](docs/manual/interface.md#sending-a-job-to-another-model) — `task` takes a `model`, for cheaper or faster subagents
- [Custom agents](docs/manual/interface.md#custom-agents) — named subagent profiles in `.janetcode/agents/`; `/agents` and `ctrl+t` for the live overlay, `/agents new <brief>` to start one
- [Remote workspace](docs/manual/interface.md#remote-workspace) — `/ssh` and `/docker` retarget file tools and bash without rebuilding the agent
- [Prompt caching](docs/manual/interface.md#prompt-caching) — a byte-stable request prefix so every provider's cache hits
- [Asking for a shape](docs/manual/interface.md#asking-for-a-shape) — replies constrained to JSON where they are parsed rather than read
- [File viewer and hunk review](docs/manual/interface.md#file-viewer-and-hunk-review) — `/open path:line` and `/diff`, with per-hunk revert
- [Repository map](docs/manual/interface.md#repository-map) — a ranked map of the repository in every request, and a `repo_map` tool
- [Change provenance](docs/manual/interface.md#change-provenance-changes-blame-undo-n) — every change recorded per tool call; `/changes`, `/blame`, `/undo n`
- [Undo by description](docs/manual/interface.md#undo-by-description) — `/undo about <description>` previews only the session's changes matching it, `/undo apply` reverts that preview and re-runs the checks
- [Proof in the commit](docs/manual/interface.md#proof-in-the-commit) — a commit this session makes gets a git note recording which session, turn and intent wrote each of its files and what actually demonstrated it worked; `/proof [commit]`, `janetcode --proof`
- [Recipes](docs/manual/interface.md#recipes-replay-a-turn-for-free) — crystallise a turn's tool calls into `.janetcode/recipes/<name>.json` and replay them with no model call and no tokens; `/recipe save|run|show|list`, `janetcode --recipe <name>`, a failed step hands over to the model
- [Trajectory critic](docs/manual/interface.md#trajectory-critic) — the harness watches the shape of a turn for loops and stalls
- [Browsing](docs/manual/interface.md#browsing) — web pages through `fetch_url` and `web_search`, or a browser MCP server
- [Calling tools from a script](docs/manual/interface.md#calling-tools-from-a-script) — `execute_code` runs Python that calls the tools, so a chain costs one turn
- [Inspecting the session](docs/manual/interface.md#inspecting-the-session) — `/inspect` and `--inspect` show the context, archive, verdicts and config
- [Getting back what compaction dropped](docs/manual/interface.md#getting-back-what-compaction-dropped) — archived output and conversation, searchable with `expand_output`
- [What a turn actually demonstrated](docs/manual/interface.md#what-a-turn-actually-demonstrated) — a verdict per turn from what ran, not from what the model said
- [Plan mode and read-only mode](docs/manual/interface.md#plan-mode-and-read-only-mode) — explore and plan first, or forbid changes entirely
- [Acceptance tests](docs/manual/interface.md#acceptance-tests) — `/accept <request>` writes tests first and stops; `/accept approve` locks them so no tool call can weaken them and their command gates the session
- [Verification gates](docs/manual/interface.md#verification-gates) — `.janetcode/verify.json` says what done means; failures go back to the model
- [Diagnostics from language servers](docs/manual/interface.md#diagnostics-from-language-servers) — compile and type errors after every edit, when a server is on PATH
- [The reach of an edit](docs/manual/interface.md#the-reach-of-an-edit) — a permission prompt for `edit_file`/`write_file` shows the symbols it changes, their references, their tests and the checks that will run, from the language server or the repository map, within a fixed short budget
- [Hooks](docs/manual/interface.md#hooks) — shell commands at fixed points of the loop that can block or rewrite a call
- [MCP servers](docs/manual/interface.md#mcp-servers) — stdio and remote servers, with OAuth, deferred tools and a trust store
- [Custom commands](docs/manual/interface.md#custom-commands) — prompt templates as `/name` from `.janetcode/commands/`
- [Project instructions](docs/manual/configuration.md#project-instructions) — `JANETCODE.md`, `AGENTS.md` or `CLAUDE.md`, from the working directory up to the repository root
- [Path-scoped rules](docs/manual/configuration.md#path-scoped-rules) — `.janetcode/rules/*.md` sent only when a call touches a path they name (`/rules`)
- [Keeping a session as an eval](docs/manual/evals.md#keeping-a-session-as-an-eval) — `/eval keep [name] [--check cmd]` turns a real session into a regression eval task, replayed later with `janetcode --eval .janetcode/evals`
- [What if: replaying a session with one thing changed](docs/manual/sessions.md#what-if-replaying-a-session-with-one-thing-changed) — `janetcode --what-if <session> --from-turn <n> [--model m] [--off names] [--deny rule] [--prompt text]` replays the turns before `n` for free and runs the rest live against the change, then reports where the two runs first diverge, per-turn verdicts, files changed, a check run in the worktree, tool calls, tokens and cost

## What has been measured

Numbers live in `docs/benchmarks/`, with the runs that produced them.

- **Against Claude Code**, on Claude models, all twelve cells passing: on
  haiku janetcode read 2.6M tokens to Claude Code's 4.2M and cost $0.54
  against $1.49; on sonnet the token counts are a dead heat (3.25M against
  3.23M) but Claude Code got there in 80 model calls to janetcode's 173.
  `docs/benchmarks/harnesses/claude/`.
- **Against other harnesses**, same model and endpoint: on three
  multi-prompt sessions every harness passed every task, and the context
  read to get there was janetcode 985k, pi 1.95M, grok 3.31M, qwencode
  16.4M. The gap is mostly fixed per-call overhead — janetcode's prompt
  and schemas come to about 1.8k tokens, against 18k, 23k and 70k.
- **Across eleven models**, five harnesses (janetcode, pi, hermes-agent,
  qwencode, openclaw), three runs each — 3,300 task runs on 2026-09-10:
  janetcode passed 651/660, the most, at $1.17 at list price for all
  eleven, the lowest cost per solved task (0.18¢ against hermes-agent
  0.25¢, pi 0.47¢, qwencode 1.19¢, openclaw 1.28¢), first or second on
  cost on every model. Its context read stays near 1M tokens per 60 tasks
  on every model while qwencode and openclaw read 7–12M.
  `docs/benchmarks/harnesses/eleven-models/`.
- **The hard set across harnesses** (tasks 21–26, qwen3.7-plus, three
  runs): 87/90 passed — janetcode, pi and openclaw 18/18 — so it does not
  separate harnesses on this model either. janetcode's fresh input is
  again lowest (qwencode 6x) and its output is twice qwencode's, spent
  writing and running its own tests on the INI parser with hidden tests,
  which it passed 3/3 where qwencode passed 1/3.
  `docs/benchmarks/harnesses/qwen-hard/`.
- **The session set across five harnesses** (three sessions of six or
  seven dependent prompts, qwen3.7-plus, two runs): every harness that
  continues a session passed all six; hermes-agent, which cannot, passed
  two. Context read for the six sessions: janetcode 2.0M, pi 3.9M,
  openclaw 6.1M, qwencode 34.2M — the widest gap of any set, and at list
  price $0.22 against $0.37, $0.55 and $3.64.
  `docs/benchmarks/harnesses/qwen-session/`.
- **Against Claude Code on the same model** (qwen3.7-plus through
  OpenRouter for both, Claude Code via `cmd/modelproxy` under an empty
  config, easy/hard/session sets, three runs each): janetcode 87/87,
  Claude Code 77/87, and janetcode reads 2.4–6.2x less context, takes
  fewer model calls on every set and costs 1.6–2.7x less at list price.
  Eight of Claude Code's ten misses are files written outside the task
  directory: its 27,700-character prompt states the directory once and
  this model loses it. `docs/benchmarks/harnesses/claude-qwen/`.
- **Compaction** is safe on this set: forcing it on tasks that would not
  otherwise reach the threshold lost no task, 11/11 either way.
- **Recall after compaction**: 0/16 exact facts with no `expand_output`
  (the compacted context alone), 9/20 with it, every correct answer
  grounded in what was fetched.
- **What turns demonstrate**, replayed over 97 real turns: 42% verified,
  and the false-accusation count went from 13 to 0. Commit `3ee2021` is
  the rewrite those numbers led to, with the cases distilled into
  `internal/agent/verdict_test.go`; the replay itself was a one-off
  against live sessions and is not in the repository.

One caution the benchmark makes plain: running the session set three times
gives 940k, 774k and 843k — a **21% spread between identical runs**, and
113% on the search-heavy task. Single-run comparisons on it are not
evidence, and anything reported from it should be `--eval-runs 3` or more.

## Development

```sh
make build     # bin/janetcode
make test
make race
make lint
make check     # gofmt, vet, tests, and windows/macOS compile-checked
```

Layout:

```
cmd/janetcode         flags and wiring
internal/llm          provider-neutral messages, tools, streaming events
internal/llm/anthropic  Messages API via the official SDK
internal/llm/openai   chat-completions client (hand-rolled, dialect-tolerant)
internal/tool         tool interface, registry, and the built-in tools
internal/agent        the model/tool loop, permissions, compaction
internal/session      JSONL persistence
internal/tui          bubbletea interface
internal/headless     one-shot runner
internal/diff         unified diffs for previews
```

All tests run against fake servers; no network or API key is needed.
