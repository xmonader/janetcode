# Command-line reference

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

```
janetcode [flags] [prompt]        interactive session in the working directory
janetcode -p "prompt" [flags]     one prompt, headless, then exit
```

`janetcode --help` shows the dozen flags most people need; `janetcode
--help all` prints every flag, grouped, as the table below does.

| flag | what it does |
|---|---|
| `-p "<prompt>"` | run one prompt headless; assistant text on stdout, tool activity on stderr |
| `--output-format text\|json\|stream-json` | with `-p`: how the run reports itself (see below) |
| `--input-format text\|stream-json` | `stream-json`: a conversation on stdin, a turn per line (see [Streaming input](#streaming-input)); needs `--output-format stream-json`, and `-p` becomes optional |
| `--json-schema <json\|@file>` | with `-p`: answer with JSON matching this schema (see [Structured output](#structured-output)); exits 54 when no valid answer comes |
| `--max-cost <usd>` | with `-p`: stop once the run has spent this much, and exit 55. Checked after every model call, so a long turn stops within one call of the ceiling; a model nothing can price is not held to it |
| `--max-turns <n>` | cap the model calls one turn may make; the eval runner's `--eval-max-turns` with the everyday spelling, so a benchmark can give a headless run and an eval run the same budget |
| `--compact-at <tokens>` | compact the history at this size instead of the configured one; `--eval-compact-at` with the everyday spelling |
| `--model`, `--provider` | override the configured model or backend for this run |
| `--effort` | reasoning effort where the provider supports it |
| `--plain` | draw the terminal interface in ASCII (`+-|` borders, `>` and `*` marks) with a spinner that does not move, for the Linux console, a font without box drawing, or a quieter screen. `JANETCODE_PLAIN=1` does the same, and it is on by itself when `TERM=linux`; `NO_COLOR` turns colour off separately. For a screen reader, `--web` is the better interface: it has landmarks and live announcements |
| `--theme <name>` | colour theme for this run: `auto`, `dark`, `light`, or a builtin or user theme name. Overrides the config for this run only; `--inspect themes` lists them |
| `--yolo` | run mutating tools without asking |
| `--auto` | auto mode: in-workspace file edits and known-safe reads run without asking; other commands still ask. `/auto` toggles it; shift+tab cycles ask → plan → auto → yolo |
| `--sandbox` | wrap local bash in docker (or sandbox-exec on macOS). File tools stay on the workspace host. Off by default; `/sandbox` toggles it |
| `--auto-edits` | auto-accept file edits; commands still ask. `/edits` toggles the same thing mid-session |
| `--readonly` | read, search and answer, but change nothing |
| `--append-system-prompt "<text>"` | add text (or `@file`) to the system prompt for this run (TUI sessions too); `JANETCODE_SYSTEM_MD=<file>` replaces the base prompt instead. Both are recorded in eval results, so a prompt A/B stays traceable |
| `--continue` (or `-c`), `--resume <id>` (or `-r`) | carry on the last session here, or a named one (including one started with `--bg`) |
| `--bg` | run the prompt in the background, print the session id, and return; reopen with `--resume`. What its permission mode asks about waits for `--answer`; it runs `--yolo` only when given (see [sessions](sessions.md)) |
| `--answer <id> [allow\|session\|deny]` | show, or settle, the permission request a `--bg` run is waiting on. `JANETCODE_NOTIFY=<command>` runs a command each time one starts waiting |
| `--add-dir <path>` | extra local directory file tools may read and edit this session (repeatable) |
| `--set <key>=<value>` | set a config key for this run only, repeatable: the keys `/config` takes, including the model slots (`route_cheap`, `strong_model`, `advisor_model`, `vision_model`), the features that use them (`escalate_after`, `advisor_mode`, `stale_check_nudge`) and the decisions block (`decisions.enabled`, `decisions.model`, `decisions.endpoint`, `decisions.triage`, …). Nothing is written to the config file, and a key or value the file would be refused for is refused here |
| `--replay <session>` | re-run a saved session against the current code, with the model's replies read from the recording instead of bought: it reports the tool calls it makes now against the ones recorded, and needs no API key |
| `--what-if <session>` | counterfactual replay: see [What if: replaying a session with one thing changed](sessions.md#what-if-replaying-a-session-with-one-thing-changed) |
| `--from-turn <n>` | with `--what-if`: the first turn (1-based) to run live instead of from the recording |
| `--off <names>` | with `--what-if`: comma-separated behaviours to disable for the live part, the same names as `--eval-off` / `JANETCODE_OFF` |
| `--prompt "<text>"` | with `--what-if`: replacement text for `--from-turn`'s prompt, instead of the session's own recorded one |
| `--what-if-keep` | with `--what-if`: leave the worktree on disk instead of removing it, and print its path |
| `--json` | with `--what-if`: write the comparison report as JSON to stdout instead of text |
| `--sessions` | list the sessions recorded for this directory |
| `--worktree <branch>` | run the session in a git worktree on that branch |
| `--until "<cmd>"` | keep going until the command exits 0, at most five rounds |
| `--watch "<cmd>"` | stay running, re-run the command when files change, fix failures |
| `--acp` | speak the Agent Client Protocol on stdio, for an editor |
| `--web` | serve the session in a browser instead of the terminal |
| `--web-addr <host:port>` | listen address for `--web` (default `127.0.0.1:8787`). Loopback unless you name another host. The page requires the access token in the printed URL, on loopback too, since other users of the machine can reach it (`JANETCODE_WEB_TOKEN` fixes it) |
| `--inspect <subject>` | print one part of the session state and exit; `list` names them |
| `--inspect features` | print the whole feature catalogue: commands, keys, flags, tools, config keys. `--inspect "features <query>"` searches it and the embedded manual |
| `--doctor` | check config, credentials, providers, tools and report what is wrong. It ends with the repairs it could make itself, as `would fix: …` lines |
| `--doctor --fix` | carry out those repairs and print each change. Only the safe, obvious ones: a config key one or two letters from a real one, a missing config directory, an MCP login that has expired and has no refresh token, a directory of ours the owner can no longer read, a config file other users can read (made 0600). Credentials, which provider to use and the project's own files are reported and never changed. A `.janetcode/verify.json` that was never trusted is listed as `needs you:` with its commands and is not trusted for you: its commands are the repository's and run after every change, so read them and run `/verify trust` yourself. Running it twice changes nothing the second time |
| `--doctor --output-format json` | the same checkup as one JSON object: `ok`, `failed`, each check with its `status` (`ok`, `fail`, `warn`, `info`) and `detail`, and the `repairs`. Exits non-zero when a check fails, like the text form |
| `--backup <file>` | write the config directory (config, permission rules, MCP trust and logins) and the data directory (sessions, memory, archives) to one tar.gz, mode 0600: it holds your API keys. Symlinks are left out |
| `--restore <file>` | put a `--backup` archive back. Files it holds are overwritten, files it lacks are left alone, and the files it replaces are first saved to `janetcode-before-restore-<time>.tar.gz` beside it, so `--restore` on that file undoes it. An archive with a path outside config/ and data/, or anything but plain files, is refused before anything is written |
| `--proof [commit]` | print the proof note attached to a commit (default `HEAD`): which session, turn and intent wrote each of its files, and what actually demonstrated the work; "no proof for `<sha>`" when none is attached, then exit |
| `--recipe <name> [k=v ...]` | replay a saved recipe (`.janetcode/recipes/<name>.json`, see [Recipes](interface.md#recipes-replay-a-turn-for-free)) as tool calls, no model call and no tokens unless a step fails or, once every step has succeeded, the project's own [verify gate](interface.md#verification-gates) fails; `k=v` substitutes the recipe's parameters. Either failure hands over to the configured model the way `-p` running under `--until` does, or exits non-zero without one. MCP servers are started first only when a step calls one of their tools |
| `--init` | write a starter config file |
| `--init-project` | write a starter `JANETCODE.md` for this repository, from its languages, declared commands and layout, and exit. An existing one is never overwritten; `/init force` in a session replaces it. See [Project instructions](configuration.md#project-instructions) |
| `--allow`, `--deny` | add a permission rule for this run |
| `--mcp-auth <server>` | run the OAuth flow for an MCP server |
| `--update` | replace this binary with the latest release: the download is checked against the release's `SHA256SUMS` and written beside the old binary before it is renamed over it. A build from source says it is not a release; `JANETCODE_RELEASES` points at a mirror |
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
  `retry`, `discard`, `compacted`, `verdict`, `turn_done` — and ends with the same
  `result` object as its last line. Everything is newline-delimited, so it
  can be read a line at a time. A `retry` line carries `attempts`,
  `wait_ms` and the provider's reason in `text`; it is written when the
  call finally answers, since until then there is nothing to report.
  A `discard` line means the model call that streamed the `text` lines
  before it was cut off mid-stream: those deltas are void, and the retry
  streams its whole answer again after it.
  Every `tool_result` is preceded by a `tool_start` with the same `id`,
  including for a call that never ran — refused by a rule, hidden by
  `--readonly`, stopped by `--max-cost`, or naming a tool that does not
  exist — so a consumer can pair the two without special cases.
  `permission_denials` counts every refusal, whoever made it: a `--deny`
  rule, a hook, read-only mode, or the person at the terminal.

## Streaming input

`--input-format stream-json` keeps one process for a whole conversation.
A program writes one JSON object per line on stdin; each user message is
a turn in the same session, and each turn ends with its own `result`
line, so a driver knows when to send the next. The process exits at EOF
with the last turn's exit code. It needs `--output-format stream-json`;
a `-p` prompt, if given, runs first.

```text
{"type":"user","text":"read go.mod and say which Go version"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"now bump it"}]}}
{"type":"control","subtype":"interrupt"}
{"type":"control_request","request_id":"r1","request":{"subtype":"interrupt"}}
```

The `message` and `control_request` shapes are Claude Code's, so a driver
written for it works unchanged. An interrupt cancels the running turn —
its result line has `is_error` true and exit code 130 — and the session
carries on with the next message; it is answered by a `control_response`
line carrying the `request_id`. Messages sent while a turn runs queue
behind it. A line that does not parse, or that carries a non-text block,
is reported as an `input_error` line and skipped. The counters in each
result (usage, cost, tool calls) are the session's so far, not the
turn's. Nothing can be asked on stdin, so a call the rules and approval
mode do not allow is denied: pick the mode with `--auto`, `--yolo` or
`--allow`.

## Structured output

`--json-schema` turns the answer into data. The schema is inline JSON or
`@path` to a file, and is checked before any model call. The model gets
one more tool, `structured_output`, whose input schema is yours; calling
it is how the answer is delivered. The harness validates each call
itself — many OpenAI-compatible servers ignore constrained decoding — and
a call that does not match goes back to the model with every violation
listed. A turn that ends without a valid call is nudged twice more, then
the run exits 54.

```sh
janetcode -p "how many Go files are under internal/?" \
  --json-schema '{"type":"object","properties":{"count":{"type":"integer"}},"required":["count"]}' | jq .count
```

With `text` output, stdout holds the value alone and the model's prose
goes to stderr. With `json` and `stream-json` the value is the result
object's `structured_output` field. A schema whose top level is not an
object is accepted: the tool wraps it as `{"result": ...}` and the value
comes out unwrapped. Supported keywords: `type`, `properties`,
`required`, `additionalProperties`, `items`, `prefixItems`, `enum`,
`const`, the numeric and length bounds, `pattern`, `uniqueItems`,
`anyOf`/`oneOf`/`allOf`/`not`, and local `$ref` into `$defs` or
`definitions`. Unknown keywords are ignored; a remote `$ref` is refused.

## Exit codes

| code | meaning |
|---|---|
| 0 | the run finished |
| 1 | anything else |
| 41 | no API key, or the endpoint rejected it (HTTP 401/403) |
| 42 | the flags or the prompt do not make sense |
| 52 | the configuration file cannot be used |
| 53 | the turn hit its model-call cap |
| 54 | no answer matched `--json-schema` |
| 55 | the run reached `--max-cost` |
| 130 | interrupted, as a shell reports a SIGINT |

The code is also in the `exit_code` field of the result object, so a
caller reading only stdout still has it.
