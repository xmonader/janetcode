# Command-line reference

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

```
janetcode [flags] [prompt]        interactive session in the working directory
janetcode -p "prompt" [flags]     one prompt, headless, then exit
```

| flag | what it does |
|---|---|
| `-p "<prompt>"` | run one prompt headless; assistant text on stdout, tool activity on stderr |
| `--model`, `--provider` | override the configured model or backend for this run |
| `--effort` | reasoning effort where the provider supports it |
| `--yolo` | run mutating tools without asking |
| `--readonly` | read, search and answer, but change nothing |
| `--continue`, `--resume [id]` | carry on the last session here, or a named one |
| `--sessions` | list the sessions recorded for this directory |
| `--worktree <branch>` | run the session in a git worktree on that branch |
| `--until "<cmd>"` | keep going until the command exits 0, at most five rounds |
| `--watch "<cmd>"` | stay running, re-run the command when files change, fix failures |
| `--acp` | speak the Agent Client Protocol on stdio, for an editor |
| `--inspect <subject>` | print one part of the session state and exit; `list` names them |
| `--doctor` | check config, credentials, providers, tools and report what is wrong |
| `--init` | write a starter config file |
| `--allow`, `--deny` | add a permission rule for this run |
| `--mcp-auth <server>` | run the OAuth flow for an MCP server |
| `--version` | print the version |

Eval flags (`--eval` and the rest) are documented under [Evals](evals.md).
