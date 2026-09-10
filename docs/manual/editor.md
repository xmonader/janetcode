# Editor integration (ACP)

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
