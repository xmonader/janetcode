# janetcode

A terminal coding agent in a single Go binary. It reads and edits code, runs
commands, and iterates on the result, with every mutating action shown to you
as a diff before it happens. Works with Anthropic's Messages API and any
OpenAI-compatible endpoint (OpenAI, xAI, Qwen, DeepSeek, OpenRouter, vLLM,
Ollama).

## Install

```sh
git clone https://github.com/xmonader/janetcode
cd janetcode
make install        # puts janetcode on your $GOPATH/bin
```

Requires Go 1.25+.

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
| `edit_file` | Replace an exact string in a file | yes, shows a diff |
| `write_file` | Create or overwrite a file | yes, shows a diff |
| `bash` | Run a shell command with a timeout | yes |
| `fetch_url` | Fetch a web page or API response as text | yes |
| `task` | Delegate a read-only job to a subagent with its own context; several run in parallel | no |

All paths are confined to the working directory, symlinks included.
Read-only tools run concurrently; mutating tools run one at a time, in order.

## The interface

The transcript streams the model's markdown as it arrives, shows each tool
call on one line with what it did and how long it took, and renders every
edit as a coloured diff. The status line shows what the agent is doing, the
elapsed time, and a gauge of how full the context window is. The input box
grows with your text.

```
enter            send        ctrl+j / alt+enter   newline
up / down        recall earlier prompts          tab   complete a /command
esc              cancel the running turn (or clear the input)
ctrl+o           expand tool output and diffs    ctrl+l   redraw
pgup / pgdn      scroll (mouse wheel works)
ctrl+c           cancel; twice to force quit; quits when idle
```

At a permission prompt: `y` allow once, `a` allow that tool for the session,
`n` deny and type why (the reason goes to the model, enter skips it), `esc`
deny silently. Destructive shell commands (recursive deletes, force pushes,
sudo, piping downloads into a shell) are flagged in the prompt.

Typing while the model is working queues the message; it is sent as soon as
the turn ends. Cancelling a turn hands queued text back to the input.

```
@path            attach a file, an image (png/jpg/gif/webp), or a directory listing; tab completes
ctrl+v           attach the image in the clipboard (wl-paste or xclip on Linux, pngpaste on macOS)
!command         run a shell command here; its output is shown and shared with the model
/help      /quit      /clear (new session)     /yolo (toggle auto-approve)
/plan <request> (plan mode: explore, write a plan; /plan approve executes it)
/readonly  (toggle read-only: the model can inspect but change nothing)
/connect   /model     /compact (summarise the context to free the window)
/usage     /stats (turns, model calls, tool calls per tool, tokens, cost)
/resume    /verbose   /resume <n or id> (reopen an earlier session from this directory)
/undo [preview]  /redo  /find X    /cost      /mcp [trust]
/fork [title]  /title <text>  /history <words> (search every saved session)
/open path[:line] (view a file inline)   /diff [path | revert path:n] (hunk review)
/context   (what fills the window)   /pin <note or @file>   /unpin <n>
/export [file.md] (write the conversation as a Markdown transcript)
```

`ctrl+c` cancels a running turn, then clears typed text, then quits.

`/connect` walks through provider → base URL → API keys → model with
arrow-key lists that filter as you type; `/model` alone opens just the model
list for the current provider (`/model name` sets one directly). Both write
the choice to the config file. When the provider's model list carries a
context length and prices (OpenRouter does), the list shows them
(`128k ctx · $0.15/$0.6 per M`) and the choice is saved under `"models"`
in the config, so cost estimates work without a `"prices"` entry and
compaction runs at three quarters of the model's window (capped at 200k
tokens) instead of the 80k default. Explicit `"prices"` and `compact_at`
still win; Claude and GPT windows are known without a listing.

Safety rules that apply in every mode, including `--yolo`: commands that
would destroy the machine (deleting `/` or the home directory, formatting,
writing raw devices, piping a download into a shell, fork bombs, power-off)
are refused outright, seen through `sudo`, `env`, `timeout`, `sh -c` and
`eval` wrappers; commands that change files outside the working directory
(`rm ../x`, `cp a ~/b`, `> /etc/hosts`) always ask. Everything else follows
the permission mode.

`/undo` puts the work tree back to how it was before the last turn that
changed anything, and tells the model. Inside a git repository the whole
tree is snapshotted before the first mutating tool of a turn (a hidden
commit under `refs/janetcode/cp/`, taken through a temporary index so
HEAD, your index and your branches are never touched), so changes made by
shell commands, generated files and deletions all revert. Outside git the
files changed through `edit_file`/`write_file` are restored from copies.
Repeat it to go back further (up to 30 turns); it still works after
`--continue` or `/resume`. If you committed or checked out in between, the
git snapshot is not applied and only file-tool changes are reverted.
`/undo preview` lists what an undo would restore, recreate or remove
without touching anything. `/redo` reapplies what the last `/undo`
reverted; it survives a restart but is discarded as soon as the next turn
changes something, because the two histories would no longer line up.

### Permission rules

Approval is normally per call (`y`), or per tool for the session (`a`).
Press `r` at a prompt to save a rule instead, so matching calls never ask
again in any session; the prompt shows the rule it would save, such as
`bash(go test *)`, `edit_file(internal/agent/*)` or
`fetch_url(https://pkg.go.dev/*)`. Rules live under `"permissions"` in the
config file:

```json
"permissions": {
  "allow": ["read_file", "bash(go test *)", "bash(make *)", "edit_file(internal/*)"],
  "deny":  ["bash(git push*)", "bash(rm -rf *)"]
}
```

A rule is `tool` or `tool(pattern)`; `*` spans anything and `?` one
character, matched against the command for `bash`, the path for file
tools and the URL for `fetch_url`. Deny wins over allow; a denied call is
refused without a prompt and the model is told which rule did it.
Guarded commands (ones that reach outside the workspace) still ask.
`/permissions` lists rules, `/permissions allow <rule>` and `/permissions
deny <rule>` add one. A project can ship `.janetcode/permissions.json`
with the same shape: its deny rules always apply, its allow rules only
after `/permissions trust`. Headless runs take `--allow` and `--deny`
with comma-separated rules, and a `pre_tool` hook can answer
`{"decision":"allow"}` to approve a call itself.

### Memory

janetcode remembers across sessions. Each entry is a small markdown file
with frontmatter (`name`, `description`, `type`, `source`, `uses`, dates)
under `~/.local/share/janetcode/memory/`: a project scope keyed by project
identity (`.janetcode/project` if present, else the git origin URL, else
the path) and a global scope (`_global/`) for things about you that hold
everywhere. Types: `user`, `feedback`, `project`, `decision`, `reference`.

- **Index in the prompt.** Names and one-line descriptions of every
  entry go into the system prompt (8 KB budget). Bodies do not, so the
  prompt stays small no matter how much is remembered.
- **Recall per turn.** Entries whose names, descriptions or bodies overlap
  the request clearly are attached to that turn (up to four, 6 KB); you
  see `memory recalled: …`. Recalled entries count as used, and the least
  used, oldest entries are the ones trimmed past 200 per scope.
- **The model has a `memory` tool**: `recall` by query, `remember` when
  you tell it something worth keeping (it picks type and scope), `forget`.
- **Automatic extraction.** After a turn that changed something, or one
  where you gave feedback ("never…", "always…", "prefer…"), a cheap extra
  call asks what the conversation *established*, as typed JSON entries
  that may also replace an entry that became wrong. Duplicates and
  near-duplicates merge instead of piling up. `"memory": false` turns it
  off; `/remember` and the tool still work.
- `/memory` lists entries, `/memory <words>` recalls, `/memory show <name>`
  prints one, `/remember [-g] <fact>` adds (`-g` global), `/forget
  <name|number>` removes. Files are plain markdown you can edit by hand.

Recall matches words by default. With `"embedding_model": "text-embedding-3-small"`
(or any model your provider serves at `/v1/embeddings`) it also ranks
entries by meaning, so "verify the build works" finds a note about running
`make test`. Vectors are cached next to the entries and recomputed only
when an entry changes; if the endpoint fails, recall silently falls back to
words and retries a few minutes later.

### Skills

A skill is a markdown file with instructions for one kind of task
(`.janetcode/skills/<name>/SKILL.md` or `.janetcode/skills/<name>.md` in
the project, `skills/` under the config directory for all projects;
`.claude/skills` is read too). A directory skill can ship scripts and
reference files next to `SKILL.md`; they are listed with absolute paths
when the skill is attached so the model can read or run them. Optional
frontmatter gives `name`, `description`, comma-separated `keywords` (or
`tags`), `disable-model-invocation: true` (only you can invoke it) and
`user-invocable: false` (only the model can):

```markdown
---
description: Ship a release to production
keywords: release, ship
---
1. `make test` must pass ...
```

Every model-invocable skill is listed by name and description in the
system prompt. When a request clearly matches one (its name or a keyword
in the text, or several words of its description and headings), the file
is attached to that turn (up to five, not re-attached while still fresh
in the context) and you see `skills attached: …`. For anything else the
model can read a skill with the `skill` tool. You can run one yourself as
`/<name> args` (`$ARGUMENTS` in the body is replaced), and `/skills`
lists what is installed. Skills are re-read from disk every turn, so
editing one takes effect immediately.

### Background jobs

`bash` with `background: true` starts a command detached (its own
process group, output to a log file) and returns a job id at once, so a
dev server or a long build does not block the turn. When the job ends,
the model is told before its next call and you see `⚙ job 1 finished with
exit 0` in the transcript, with the last lines of output. The `job` tool
lists jobs, shows output, waits with a timeout, or kills one; `/jobs` and
`/jobs kill <n>` do the same for you. Every job is stopped when janetcode
exits.

### Editing subagents in a worktree

`task` with `edit: true` gives the subagent the full tool set inside a
throwaway `git worktree` of the project (uncommitted changes included, via
a snapshot). It edits and runs commands there without asking, and the
main agent gets back its report plus the diff: a `--stat` summary, the
patch inline, and the patch saved to a file so it can be applied with
`git apply` after review. The real tree is untouched until then, and
your deny rules still apply inside the worktree. This needs a git
repository.

### Custom agents

`.janetcode/agents/<name>.md` (or `agents/` under the config directory,
or `.claude/agents`) defines a subagent the model can pick with
`task {"agent": "<name>", ...}`: the body is added to the subagent's system
prompt, and the frontmatter may set `description`, a `tools` list
(read-only tools only) and a `model`. The `task` tool's description lists
them, so the model knows they exist.

```markdown
---
description: Reviews a change for correctness and missing tests
tools: read_file, grep, glob
---
You are a strict reviewer. Report concrete defects with file:line ...
```

### Prompt caching

The request prefix is laid out to stay byte-identical between calls, so
providers that cache prompts (Anthropic explicitly, OpenRouter and others
automatically) serve most of the history from cache: the system prompt is
fixed for the session, the memory and skill indexes inside it are evaluated
once and refreshed only at compaction (or with `/reload`), tool definitions
are static and sorted, and skills and recalled memory are attached as user
blocks rather than by editing the prompt. Pins, plan mode and read-only
mode do change the prompt, and each change costs one cache miss. The
footer shows the session's cache hit rate (`cache 87%`) whenever the
provider reports cached tokens.

### File viewer and hunk review

`/open internal/x/x.go:120` shows the file inline with line numbers,
centred on the line. `/diff` lists what differs from HEAD (files with
hunk counts, new and deleted files); `/diff path` shows that file's
hunks, numbered; `/diff revert path:2` puts one hunk back the way HEAD
has it, leaving the rest of the file's changes in place. `/undo` still
reverts a whole turn at once.

### Context inspector and pins

`/context` shows where the window goes: system prompt, memory and skill
indexes, tool definitions, user and assistant text, tool calls and
results by tool, and the five largest tool results with the message they
sit in. `/pin <note>` or `/pin @path` keeps a note (or a file's content,
up to 16 KB) in the system prompt for the rest of the session; pins survive
compaction and session resume, `/pin` lists them and `/unpin <n>` drops
one.

### Plan mode and read-only mode

`/plan add rate limiting to the API` puts the session in plan mode: the
model only gets read-only tools, explores the code and writes a numbered
plan under `## Plan` (files, order, verification, risks). Ask questions or
request changes to the plan as normal messages; `/plan approve` leaves the
mode and tells the model to execute the plan step by step, verifying each
step. Every step is a normal turn, so `/undo` walks it back. `/plan off`
leaves without executing.

`/readonly` (or `--readonly` on the command line) is the same restriction
without the plan: useful for reviews and for letting the model answer
questions about a codebase you do not want touched. In both modes a
mutating call the model tries anyway fails with a message rather than
asking you.

### Verification gates

A project can define what "done" means. `.janetcode/verify.json` lists
commands that must pass after any turn that changed something:

```json
{"commands": ["go build ./...", "go test ./..."], "timeout": 600}
```

When the model ends a turn after editing or running something, the
commands run in order. A failure is shown to you and fed back to the model
as a message with the exit code and output, and it keeps working; after
three failed rounds it stops and tells you. A pass shows as
`verified: …`. Read-only turns skip the checks, and the checks do not
rerun until something changes again.

`/verify` runs the checks now, `/verify set "make test"` adds a command to
the file, `/verify clear` removes them all. Like hooks, a verify file that
came with a clone needs a one-time `/verify trust` before it runs, because
it executes commands on your machine; files you write through `/verify
set` are trusted as you write them. In headless mode `--until` remains the
one-off equivalent.

### Diagnostics from language servers

When a language server for the file is on PATH, every `write_file` or
`edit_file` result carries its diagnostics, so the model sees a type error
in the same step that caused it rather than at the next build:

```
[diagnostics]
internal/x/x.go:14:9: error: cannot use "no" (untyped string constant) as int value in return statement (compile)
```

gopls, pyright-langserver, typescript-language-server, rust-analyzer and
clangd are used by extension; each starts on the first edit of a matching
file and is remembered as unavailable when missing. The `diagnostics` tool
re-checks a file or lists every known problem, and `/lsp` shows server
status. Configure or disable under `"lsp"` in the config file:

```json
"lsp": {
  "timeout": 8,
  "servers": {
    "pyright": {"command": ["basedpyright-langserver", "--stdio"], "extensions": [".py"]},
    "clangd": {"command": []}
  }
}
```

An empty command removes a default; `"disabled": true` turns diagnostics
off.

### Hooks

Hooks are shell commands run at fixed points of the loop. They live under
`"hooks"` in the config file (every project) or in
`<project>/.janetcode/hooks.json` (needs a one-time `/hooks trust`, like
`.mcp.json`, because a cloned repository must not run commands on your
machine by itself):

```json
"hooks": {
  "pre_tool":    [{"match": "bash", "command": "scripts/policy.sh"}],
  "post_tool":   [{"match": "write_file|edit_file", "command": "gofmt -l . | sed 's/^/unformatted: /'"}],
  "pre_compact": [{"command": "echo 'Open PR: #42'"}],
  "stop":        [{"command": "go test ./... >/dev/null || { echo 'tests fail' >&2; exit 2; }"}]
}
```

Each hook gets the event as JSON on stdin (`event`, `cwd`, `tool`, `input`,
and for `post_tool` the `output`/`is_error`, for `stop` the model's `text`,
for `pre_compact` the estimated `tokens`) plus `JANETCODE_EVENT`,
`JANETCODE_TOOL` and `JANETCODE_CWD` in the environment. `match` is a
regexp on the tool name. What the hook prints decides:

- exit code 2 blocks: a `pre_tool` call is denied with stderr as the
  reason; a `stop` sends the model back to work with stderr as the message
  (at most three times per turn).
- stdout JSON `{"decision":"deny","reason":"…"}` blocks the same way;
  `{"input":{…}}` rewrites the tool input (the rewritten input is what is
  previewed, approved and run); `{"note":"…"}` is appended to the tool
  result (or to the handoff note for `pre_compact`);
  `{"continue":"…"}` on `stop` sends the model back with that text.
- plain text on stdout is a note.
- any other exit code, a crash or a timeout (10 s by default, `timeout`
  in seconds) is logged and ignored: hooks fail open, so a broken hook
  cannot wedge a session.

`/hooks` lists what is in effect.

### MCP servers

janetcode is an MCP client over stdio. Servers listed under `mcp_servers` in
the config file start with every session. Servers listed in a project's
`.mcp.json` (the same format Claude Code uses) are shown at startup but only
start after you run `/mcp trust` once for that exact file; editing the file
asks again, because the file can run any command on your machine.

```json
{"mcpServers": {"fs": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "."]}}}
```

Remote servers use `"url"` instead of `"command"`; janetcode speaks
Streamable HTTP and falls back to the older SSE transport on its own.
`"headers"` adds request headers and expands `${ENV_VARS}`, so a token
never has to be written into the file:

```json
{"mcpServers": {"docs": {"url": "https://example.com/mcp", "headers": {"Authorization": "Bearer ${DOCS_TOKEN}"}}}}
```

Server tools appear as `mcp__<server>__<tool>`. They ask for permission
unless the server marks them read-only. Servers that publish resources get
`mcp__<server>__list_resources` and `read_resource` tools, and every prompt
a server offers becomes a slash command: `/mcp__<server>__<prompt> args`.
`/mcp` shows status, tools and prompts.

### Custom commands

Drop a prompt template at `.janetcode/commands/<name>.md` in the project (or
`~/.config/janetcode/commands/<name>.md` for every project) and `/<name> args`
submits it with `$ARGUMENTS` replaced. The first line is the help text shown
in tab completion.

## Configuration

Precedence: command-line flags > environment > config file.

```sh
janetcode --init     # writes ~/.config/janetcode/config.json
```

```json
{
  "provider": "anthropic",
  "model": "claude-opus-5",
  "max_tokens": 16000,
  "effort": "high",
  "auto_approve": false,
  "max_iterations": 50,
  "compact_at": 80000,
  "memory": true,
  "theme": "auto",
  "providers": {
    "anthropic":  { "type": "anthropic", "api_key": "sk-ant-..." },
    "openrouter": { "type": "openai", "base_url": "https://openrouter.ai/api/v1",
                    "api_key": "sk-or-1", "api_keys": ["sk-or-2"], "model": "deepseek/deepseek-v4-flash" },
    "ollama":     { "type": "openai", "base_url": "http://localhost:11434/v1" }
  }
}
```

`providers` holds any number of backends by name; `provider` selects one.
`type` is `anthropic` or `openai` (any OpenAI-compatible server). A preset
name needs no `base_url`. `api_keys` lists extra keys to rotate through on
429/401/403 responses, and each entry remembers its last `model`, so
`--provider openrouter` comes back with the model last used there. The older
`anthropic`/`openai` sections are still read.

Environment: `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL` (a key may be a comma-separated list), `JANETCODE_PROVIDER`,
`JANETCODE_MODEL`, `JANETCODE_EFFORT`, `JANETCODE_THEME`. Keys from the
environment are never copied into the config file.

`effort` (Anthropic only) is `low`, `medium`, `high`, `xhigh` or `max`.
Adaptive thinking is on for Claude 4.6+ models and off for Haiku; set
`"thinking": false` to override.

### Project instructions

If the working directory contains `JANETCODE.md`, `AGENTS.md` or `CLAUDE.md`
(first found wins), its contents are added to the system prompt.

## Sessions

Every conversation is saved as append-only JSONL under
`~/.local/share/janetcode/sessions/`.

Every session gets a title after its first turn: the model is asked for a
few words (a headless run uses the first prompt instead), and `/title
<text>` renames one. `/fork [title]` branches the conversation: a new
session starts with a copy of the messages and pins, the original stays
as it was, and `/resume` lists both so you can try two approaches from the
same point. `/history <words>` searches every saved session in every
directory for the words and shows the best matching line with the session
id to `/resume`.

```sh
janetcode --sessions        # list sessions for this directory
janetcode --continue        # resume the newest one
janetcode --resume <id>
```

Inside the TUI, `/resume` lists the sessions for the directory and
`/resume 2` reopens one.

When the context grows past `compact_at` tokens (80k by default: far below
any modern window, because a smaller working context is cheaper, caches
better and is attended to better), or the model reports the prompt is too
long, the history is summarised into a handoff note and the conversation
continues in a new session file. A "too long" error that names the model's
real limit lowers the threshold for the rest of the session. `/compact`
does this on demand.

Tool results longer than 24 KB never reach the model whole: the full text
is archived under an id such as `ctx-12` (per project, 8 MiB, oldest
evicted) and the model sees the first and last part plus the id. The
`expand_output` tool pages through an archived result, greps it, or greps
every archived result, so a truncated build log or grep can always be
recovered instead of rerun.

An MCP server that exposes more than six tools has them registered
*deferred*: their schemas are not sent with every request. The model sees
their names in the `tool_search` description and loads the ones it needs
by keyword, after which they stay available. The `task` subagent tool takes
an optional `tools` list so a narrow job gets a narrow toolset.

## Evals

`make eval` runs the tasks in `evals/*.json` against the configured
provider and prints pass/fail, time, tool calls and cost per task:

```
     task                             time  tools       cost
PASS 01-create-file                  4.2s      1    $0.0004
FAIL 03-fix-failing-test            31.0s      9    $0.0031
     check failed: exit status 1: --- FAIL: TestSumTo
…
18/20 passed · 71 tool calls · 4m12s · cost $0.06
```

A task is a prompt, optional setup files, and a shell `check` that must
exit 0 in the work directory (`EVAL_OUTPUT` names a file with the model's
final text); `max_tool_calls` fails tasks that flail. `EVAL_ONLY=go,files`
narrows by name or tag, `EVAL_JOBS=4` runs more at once, and
`--eval-json` (written to `evals/last-results.json` by `make eval`) keeps
the numbers for comparison. Evals use minimal agents: core tools, no hooks,
memory, skills or MCP, so a change in results comes from the model or the
prompts. They cost real money and are not part of `make test`.

## Development

```sh
make build     # bin/janetcode
make test
make race
make lint
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
