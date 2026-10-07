# Editor integration

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

`janetcode --acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com)
on stdin and stdout, so any ACP editor can launch it as a subprocess and
drive it: `initialize`, `session/new`, `session/load` (which replays the
stored conversation), `session/prompt` streamed back as `session/update`,
and `session/cancel`. Tool calls stream as `tool_call` and
`tool_call_update` with the protocol's kinds, a title naming what the call
is about, and the file it touches; approvals go to the editor as
`session/request_permission`, so you answer where you are working.

Nothing but protocol frames reaches stdout — logs go to stderr. Point your
editor's ACP agent configuration at the binary with `--acp`.

It is driven in the tests by Zed's own client library
(`@zed-industries/agent-client-protocol`), with every frame checked
against their published schemas, and the stop reasons, permission kinds
and tool kinds are pinned to that schema so they cannot drift. Cancelling
mid-turn returns `cancelled` and leaves the session usable; a frame that
is not JSON is answered with a parse error and the connection carries on.

## JetBrains IDEs

JetBrains AI Assistant runs ACP agents listed in `~/.jetbrains/acp.json`
(the same path on every OS). Add janetcode under `agent_servers`, with
the full path to the binary:

```json
{
  "default_mcp_settings": {},
  "agent_servers": {
    "janetcode": {
      "command": "/home/you/.local/bin/janetcode",
      "args": ["--acp"],
      "env": {
        "OPENAI_API_KEY": "sk-...",
        "OPENAI_BASE_URL": "https://openrouter.ai/api/v1",
        "JANETCODE_MODEL": "deepseek/deepseek-v4-flash"
      }
    }
  }
}
```

`env` is optional when the IDE is started from a shell that already has
the key, or when `~/.config/janetcode/config.json` names a provider.
janetcode advertises no login methods, so the IDE asks for none. What a
JetBrains client sends beyond Zed — `terminal` and `_meta` client
capabilities — is accepted. MCP servers the IDE passes in `session/new`
or `session/load` (stdio, http and sse, which janetcode advertises in
`mcpCapabilities`) are started for that session on top of janetcode's
own (`mcp_servers`, and the project's `.mcp.json` once trusted), and the
session answers once they are up (at most a minute). The IDE is your own
program, so its servers need no project trust; a managed policy's
`allowed_mcp_servers` still applies. The same holds for the Go SDK
(`Options.MCPServers`) and `--mcp-serve`, which run over ACP. The
handshake is pinned by `TestJetBrainsHandshake` and was run live against
a real model.

## VS Code

`editors/vscode` is a small extension with no build step:

- **janetcode: Open janetcode** (ctrl+alt+j) runs the terminal session in
  an integrated terminal, in the workspace folder.
- **Add Selection to janetcode** (ctrl+alt+k, editor context menu) and
  **Add This File to janetcode** (editor and explorer menus) type
  `@path (lines a-b)` into janetcode's input, which attaches the file;
  nothing is sent until you press enter.
- **Open janetcode in a Panel** starts `janetcode --web` on a loopback
  port and frames its browser interface beside the editor. The frame
  asks for the access cookie with `?embed=1`, which the server grants as
  `SameSite=None; Secure` on a loopback address only — a cross-site
  frame never sends a `Strict` cookie. `internal/webui/uitest/embed.mjs`
  checks the framed page in real Chrome.

Install it with `cd editors/vscode && npx @vscode/vsce package` and
`code --install-extension janetcode-0.1.0.vsix`; `npm run check` tests
it with plain node. Settings: `janetcode.path`, `janetcode.args`,
`janetcode.webPort`.
