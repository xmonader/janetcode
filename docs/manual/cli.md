# Command-line reference

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

```
janetcode [flags] [prompt]        interactive session in the working directory
janetcode -p "prompt" [flags]     one prompt, headless, then exit
```

| flag | what it does |
|---|---|
| `-p "<prompt>"` | run one prompt headless; assistant text on stdout, tool activity on stderr |
| `--output-format text\|json\|stream-json` | with `-p`: how the run reports itself (see below) |
| `--max-cost <usd>` | with `-p`: stop once the run has spent this much, and exit 55. Checked after every model call, so a long turn stops within one call of the ceiling; a model nothing can price is not held to it |
| `--max-turns <n>` | cap the model calls one turn may make; the eval runner's `--eval-max-turns` with the everyday spelling, so a benchmark can give a headless run and an eval run the same budget |
| `--compact-at <tokens>` | compact the history at this size instead of the configured one; `--eval-compact-at` with the everyday spelling |
| `--model`, `--provider` | override the configured model or backend for this run |
| `--effort` | reasoning effort where the provider supports it |
| `--yolo` | run mutating tools without asking |
| `--readonly` | read, search and answer, but change nothing |
| `--continue` (or `-c`), `--resume [id]` | carry on the last session here, or a named one |
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

## Output formats

A headless run is usually read by a program, and prose is a poor way to
tell one what a run cost or why it stopped. `--output-format` picks how
the run reports itself; it applies only to `-p` (and `--watch`).

- `text` (the default) is unchanged: the assistant's answer on stdout,
  tool activity, notices, the verdict and a usage line on stderr.
- `json` prints nothing on stdout until the end, then one object:

```json
{
  "type": "result",
  "harness": "janetcode",
  "result": "out.txt now holds the uppercased text.",
  "is_error": false,
  "exit_code": 0,
  "session_id": "20260911-153001-4f2a",
  "model": "claude-opus-5",
  "provider": "anthropic",
  "usage": {
    "input_tokens": 1840,
    "output_tokens": 622,
    "cache_read_tokens": 18304,
    "cache_write_tokens": 1792
  },
  "cost_usd": 0.0412,
  "cost_source": "reported",
  "num_turns": 6,
  "tool_calls": 9,
  "permission_denials": 0,
  "verdict": "tests pass",
  "duration_ms": 41230,
  "recoveries": {
    "prose_tool_calls": 1,
    "watchdog_kills": 0,
    "model_fallbacks": 1,
    "total": 2
  },
  "retries": 1
}
```

  `input_tokens` excludes cache reads, which are counted separately.
  `cost_source` is `reported` when the provider sent a figure with the
  response, `list price` when it was worked out from a rate table — an
  API-equivalent estimate, not money spent — and `unpriced` when neither
  is available. `verdict` is what the turn demonstrated, computed from
  what the harness watched rather than from what the model claimed.
  `recoveries` is the harness's repair work: `prose_tool_calls` were
  written as text and read back as calls, `watchdog_kills` are streams
  that stopped producing and were ended, `model_fallbacks` are calls that
  only succeeded on another model. A clean run reports zeroes. The field
  matters to a benchmark: a result reached after three rescues is not the
  same result as one reached without them, and a comparison blind to it
  credits the model with the harness's work. `/stats` says the same
  thing in the terminal. `retries` counts model calls that failed
  transiently — a rate limit, a server error — and succeeded on a later
  attempt; the backoff is already inside `duration_ms`, and this says how
  much of the wall time was the provider refusing rather than thinking.

- `stream-json` prints one JSON object per line as things happen —
  `text` deltas, `thinking`, `tool_start`, `tool_result`, `notice`,
  `retry`, `compacted`, `verdict`, `turn_done` — and ends with the same
  `result` object as its last line. Everything is newline-delimited, so it
  can be read a line at a time. A `retry` line carries `attempts`,
  `wait_ms` and the provider's reason in `text`; it is written when the
  call finally answers, since until then there is nothing to report.
  Every `tool_result` is preceded by a `tool_start` with the same `id`,
  including for a call that never ran — refused by a rule, hidden by
  `--readonly`, stopped by `--max-cost`, or naming a tool that does not
  exist — so a consumer can pair the two without special cases.
  `permission_denials` counts every refusal, whoever made it: a `--deny`
  rule, a hook, read-only mode, or the person at the terminal.

## Exit codes

| code | meaning |
|---|---|
| 0 | the run finished |
| 1 | anything else |
| 41 | no API key, or the endpoint rejected it (HTTP 401/403) |
| 42 | the flags or the prompt do not make sense |
| 52 | the configuration file cannot be used |
| 53 | the turn hit its model-call cap |
| 55 | the run reached `--max-cost` |
| 130 | interrupted, as a shell reports a SIGINT |

The code is also in the `exit_code` field of the result object, so a
caller reading only stdout still has it.
