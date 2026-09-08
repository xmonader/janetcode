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

`janetcode --doctor` checks the config file, every configured provider's
keys, a live call to the selected model, the sessions directory, git, local
tools and MCP servers, and exits non-zero when something is broken.

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
/connect   /model     /compact (summarise the context to free the window)
/usage
/resume    /verbose   /resume <n or id> (reopen an earlier session from this directory)
/undo      /find X    /cost      /mcp [trust]
/export [file.md] (write the conversation as a Markdown transcript)
```

`ctrl+c` cancels a running turn, then clears typed text, then quits.

`/connect` walks through provider → base URL → API keys → model with
arrow-key lists that filter as you type; `/model` alone opens just the model
list for the current provider (`/model name` sets one directly). Both write
the choice to the config file.

Safety rules that apply in every mode, including `--yolo`: commands that
would destroy the machine (deleting `/` or the home directory, formatting,
writing raw devices, piping a download into a shell, fork bombs, power-off)
are refused outright, seen through `sudo`, `env`, `timeout`, `sh -c` and
`eval` wrappers; commands that change files outside the working directory
(`rm ../x`, `cp a ~/b`, `> /etc/hosts`) always ask. Everything else follows
the permission mode.

`/undo` restores every file the last turn changed through `edit_file` or
`write_file` (files it created are removed) and tells the model. Repeat it to
go back further, and it still works after `--continue` or `/resume` because
checkpoints are saved next to the session file. Changes made by shell
commands are not tracked.

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
