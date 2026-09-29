# Extending and embedding

Part of the [janetcode manual](README.md). MCP servers, hooks, skills,
custom commands and agent profiles are covered in
[the interface](interface.md). This page is about packaging them as
plugins, and about driving janetcode from other programs.

## Plugins

A plugin bundles custom commands, skills, agent profiles, hooks and MCP
servers under one name, so they are installed, updated, turned off and
removed together. janetcode reads Claude Code's plugin layout as it is,
so a plugin written for Claude Code installs unchanged:

```
my-plugin/
  .claude-plugin/plugin.json   {"name": "my-plugin", "version": "1.0.0", "description": "…"}
  commands/*.md                custom slash commands
  agents/*.md                  agent profiles
  skills/<name>/SKILL.md       skills
  hooks/hooks.json             {"hooks": {"PostToolUse": [...]}}
  .mcp.json                    {"mcpServers": {...}}
```

`.janetcode-plugin/plugin.json` is read the same way and wins when both
exist. A directory with no manifest is still a plugin, named after the
directory, if it has any of the other parts. In the manifest,
`commands`, `agents` and `skills` can add directories (a path or a list
of paths), and `hooks` and `mcpServers` can be a path or the object
itself. Paths stay inside the plugin; one that points outside it is
reported and ignored. `${CLAUDE_PLUGIN_ROOT}` (or
`${JANETCODE_PLUGIN_ROOT}`) in a hook command or a server entry is the
plugin's own directory, so a hook can run a script the plugin ships.

```
/plugin install https://github.com/someone/review-kit
/plugin install ~/src/my-plugin            a copy; --link uses it in place, for developing one
/plugin                                    what is installed, and what each contributes
/plugin show review-kit                    everything it contains, and the exact commands it would run
/plugin trust review-kit                   let its hooks and MCP servers run
/plugin disable review-kit                 off without removing it; enable turns it back on
/plugin update [review-kit]                fetch again (all of them with no name)
/plugin remove review-kit
```

From a shell, script or CI job, `janetcode --plugin "<args>"` does the
same and exits, e.g. `janetcode --plugin "install
https://github.com/someone/review-kit"` or `janetcode --plugin list`.

A git URL is cloned, and so is a local directory that is a git
repository, so that `update` can pull. Any other directory is copied, and
links in it are left out. Everything lives under `plugins/` in the
config directory, and `plugins/plugins.json` records what is installed.

Commands, skills and agent profiles from an enabled plugin load in every
session. They come after your own and before the project's, so a project
file of the same name wins. A plugin's skills are scanned the way a
repository's are: one with a critical finding, such as a script that
posts the environment to a URL, is blocked and listed as blocked.

Hooks and MCP servers run commands on your machine, so installing a
plugin does not turn them on. `/plugin show` lists every hook command and
server command line, and `/plugin trust <name>` records those parts as
you saw them. A session started afterwards runs them as though they were
in your own config file. Your hook limits still apply, and a config
server with the same name wins. If an update changes a hook or a server,
they are off again until you trust the new version. An update that only
changes a command or a skill keeps the trust. A plugin whose hooks or
servers are not trusted is named when a session starts.

Changes take effect in the next session.

### Marketplaces

A marketplace is an index of plugins: `.claude-plugin/marketplace.json`
in a repository or directory (Claude Code's format), or a URL that serves
the JSON file itself.

```json
{"name": "team", "owner": {"name": "…"}, "plugins": [
  {"name": "review-kit", "source": "./plugins/review-kit", "description": "…"},
  {"name": "lint", "source": {"source": "github", "repo": "someone/lint-plugin"}},
  {"name": "docs", "source": {"source": "url", "url": "https://git.example.com/docs.git"}}
]}
```

```
/plugin marketplace add https://github.com/team/plugins
/plugin browse                     what the added marketplaces offer, with the install line for each
/plugin install review-kit@team
/plugin marketplace update         fetch the indexes again
/plugin marketplace remove team    plugins installed from it stay
```

A plugin installed from a marketplace follows its entry: `/plugin
update` fetches the marketplace again, then the plugin from wherever the
entry now points. A relative source must stay inside the marketplace.
Marketplace entries are published by whoever runs the marketplace. They
are not reviewed, so read `/plugin show` before `/plugin trust`.

## The Go SDK

`github.com/xmonader/janetcode/sdk` runs janetcode from a Go program.
It drives the janetcode binary over the Agent Client Protocol, the
stdio protocol an editor uses (`janetcode --acp`), so the program gets
the whole agent as the terminal has it: the same tools, permission
model, hooks, verification gates, memory and session files. There is no
second, thinner copy of the loop to fall behind.

```go
res, err := sdk.Run(ctx, sdk.Options{Dir: repo, Mode: sdk.ModeAutoEdits}, "make the tests pass")
if err != nil {
	return err
}
fmt.Println(res.Text)       // the answer
fmt.Println(res.Verdict)    // what the harness saw the turn demonstrate
fmt.Println(res.Usage.OutputTokens, len(res.ToolCalls))
```

For more than one prompt, start a client and open a session; the
conversation carries over between prompts, and the session is saved
like any other, so `janetcode --resume <id>` opens it in the terminal:

```go
c, err := sdk.Start(ctx, sdk.Options{
	Dir:  repo,
	Mode: sdk.ModeAsk,
	Approve: func(ctx context.Context, p sdk.Permission) sdk.Decision {
		if p.Kind == "edit" {
			return sdk.AllowOnce
		}
		return sdk.Deny
	},
	OnEvent: func(e sdk.Event) {
		if e.Kind == sdk.EventText {
			fmt.Print(e.Text)
		}
	},
})
defer c.Close()
s, _ := c.NewSession(ctx) // or c.LoadSession(ctx, id)
res, _ := s.Prompt(ctx, "why does TestParse fail?")
res, _ = s.Prompt(ctx, "fix it")
```

The options:

- `Binary`: the janetcode executable. The default is `janetcode` on
  `PATH`.
- `Dir`: the working directory. The default is the current directory.
- `Model` and `Provider`: override the configured model and provider,
  as `--model` and `--provider` do.
- `Mode`: what runs without asking. `ModeAsk` is the default;
  `ModeAutoEdits`, `ModeAuto`, `ModeReadOnly` and `ModeYolo` are the
  `--auto-edits`, `--auto`, `--readonly` and `--yolo` modes.
- `Approve`: answers every call the mode leaves to ask about with
  `AllowOnce`, `AllowAlways` or `Deny`. The `Permission` it gets
  carries the tool, a title, the kind (`read`, `edit`, `execute`, …),
  the input, the diff or command as a preview, and the paths touched.
  A nil `Approve` denies everything, so an unattended program never
  waits on a question nobody will answer. The model is told the call
  was refused and carries on.
- `OnEvent`: receives the streamed answer, reasoning and tool calls in
  order, plus the harness's own notices (`EventNotice`: a memory
  recalled, a claim the turn did not back up). Notices are not part of
  `Result.Text`, and neither is a stream that was cut off and resent.
  `OnEvent` runs on the goroutine that reads the connection, so it
  must return promptly. `Session.PromptWith` takes a handler for one
  turn's events alone.
- `Env`, `Args` and `Stderr`: extra environment entries (a provider
  key, say), extra flags (`--set k=v`, `--add-dir`), and where
  janetcode's diagnostics go. By default they are discarded.
- `MCPServers`: MCP servers for every session, on top of the ones in
  janetcode's config (`mcp_servers`, and the project's `.mcp.json` once
  trusted), which start too: a command (`Command`, `Args`, `Env`) or a
  URL (`Type` `http` or `sse`, `Headers`). They go to janetcode as ACP's
  `mcpServers`, and a session opens once they are up.

A `Result` holds the answer text, the stop reason (`end_turn`,
`cancelled`, `max_turn_requests`, `refusal`), every tool call with its
status and output, the token usage and reported cost, and the verdict.
The verdict is what the harness saw the turn demonstrate, not what the
model claimed; it is the same line `-p` prints. Cancelling the context
passed to `Prompt` cancels the turn over the protocol, so the agent
stops cleanly and the result says `cancelled`.

The usage and verdict ride in the ACP prompt result under
`_meta.janetcode` (`verdict`, `usage`, `toolCalls`), and a message chunk
that is the harness speaking carries `_meta.janetcode.notice` (with
`discard` when the message before it is void). An editor that does not
know the key ignores it; any other ACP client can read it too.

`examples/sdk` is a small program built on it. It streams the answer,
applies edits, and asks on the terminal about anything else:

```sh
go run ./examples/sdk -dir ~/src/project "add a test for Parse"
go run ./examples/sdk -mcp "everything=npx -y @modelcontextprotocol/server-everything" "echo PAPAYA with the echo tool"
```

## janetcode as an MCP server

`janetcode --mcp-serve` serves janetcode over MCP on stdio, so any MCP
client can hand it a task: another coding agent, an IDE, a desktop
assistant. Register it the way that client registers any stdio server;
for a client that reads `.mcp.json`:

```json
{"mcpServers": {"janetcode": {"command": "janetcode", "args": ["--mcp-serve", "--auto-edits"], "cwd": "/path/to/repo"}}}
```

It offers one tool, `janetcode_task`:

- `prompt` (required) is the task, in full.
- `session_id` continues an earlier task's conversation. Every result
  names its session, and `janetcode --resume <id>` opens the same one in
  the terminal.

The result is the answer as text, followed by one line with the
session id, the stop reason, the number of tool calls, how many calls the
permission mode refused, the verdict and the tokens used. The same
fields are in `structuredContent` for a client that reads the tool's
output schema. A client that sends a progress token gets a progress
notification as each tool call starts, and a `notifications/cancelled`
for the request cancels the task.

A server has nobody to answer a permission question, so the mode decides
everything. It is `--readonly` unless you give `--auto-edits` (file edits
run), `--auto` (in-workspace edits and known-safe commands run) or
`--yolo` (everything runs; the command guard still refuses what it always
refuses). A call the mode would ask about is refused, and the model is
told so. `--model`, `--provider`, `--effort`, `--allow`, `--deny`,
`--add-dir`, `--set`, `--sandbox` and `--append-system-prompt` apply to
every task. The tool is annotated read-only in read-only mode, so a
client that honours the hint can skip its own confirmation.

Tasks run in one janetcode child process speaking ACP, the same agent
the [SDK](#the-go-sdk) drives, started on the first call. Tasks in
different sessions run at the same time; tasks in one session run in
turn. Diagnostics go to stderr, and stdout carries only protocol
messages.
