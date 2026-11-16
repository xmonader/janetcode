# Configuration

Part of the [janetcode manual](README.md); the [README](../../README.md) has the short version.

Precedence: command-line flags > environment > config file.

```sh
janetcode --init     # writes ~/.config/janetcode/config.json
```

```json
{
  "provider": "anthropic",
  "model": "claude-opus-5",
  "max_tokens": 32768,
  "effort": "high",
  "auto_approve": false,
  "max_iterations": 300,
  "compact_at": 80000,
  "memory": true,
  "hints": true,
  "proof_notes": true,
  "update_check": true,
  "sandbox": "off",
  "theme": "auto",
  "providers": {
    "anthropic":  { "type": "anthropic", "api_key": "sk-ant-..." },
    "openrouter": { "type": "openai", "base_url": "https://openrouter.ai/api/v1",
                    "api_key": "sk-or-1", "api_keys": ["sk-or-2"], "model": "deepseek/deepseek-v4-flash" },
    "ollama":     { "type": "openai", "base_url": "http://localhost:11434/v1" }
  }
}
```

`max_iterations` caps the model calls one turn may make; the default is 300
(it was 50, then 100, which cut off real work on large repositories: a turn that hits
the cap in a headless or eval run is a failure, `ExitTurnLimit`, not a pause).
It is a backstop, not the runaway guard: the loop detector and the cost and
tool-call budgets stop a model that is going in circles long before the cap.

`web_search` scrapes DuckDuckGo's HTML endpoint by default, which needs no
key but can rate-limit heavy use. A search API is steadier:

| `provider` | key (when `key` is not set) | notes |
|---|---|---|
| `brave` | `BRAVE_API_KEY` | Brave Search API |
| `tavily` | `TAVILY_API_KEY` | snippets are page text, clipped |
| `serper` | `SERPER_API_KEY` | Google results |
| `exa` | `EXA_API_KEY` | snippets are page text, clipped |
| `searxng` | none | your own instance: set `url`, and enable the `json` format in its `settings.yml` |

```json
{ "search": { "provider": "serper" } }
{ "search": { "provider": "searxng", "url": "http://localhost:8888" } }
```

`key` may be set directly or as a `${VAR}` reference; `url` overrides
any endpoint (a proxy or a mock). A keyed request never follows a
redirect to another host, so the key goes only where you configured it.

`providers` holds any number of backends by name; `provider` selects one.
`type` is `anthropic`, `openai` (any OpenAI-compatible server), `gemini`
(Google's Gemini API, spoken natively) or `responses` (OpenAI's Responses
API). A preset name needs no `base_url`.

`gemini` keeps the `thoughtSignature` Gemini 3 puts on its function calls
and sends it back with them; its OpenAI-compatible endpoint (the
`gemini-openai` preset) drops it, which Gemini 3 answers with a 400 once a
tool has run. A call that never had a signature — one another model made
earlier in the session — carries Google's documented stand-in instead.
`responses` asks for reasoning back encrypted (`store: false`, nothing kept
on OpenAI's side) and replays it before the call it led to, so a reasoning
model keeps its line of thought across tool calls instead of starting over
after each one. Both learn from a 400 which parameters a model does not
take (a thinking level on Gemini 2.x, `reasoning` on a non-reasoning model)
and stop sending them. `api_keys` lists extra keys to rotate through on
429/401/403 responses, and each entry remembers its last `model`, so
`--provider openrouter` comes back with the model last used there. The older
`anthropic`/`openai` sections are still read.

An `anthropic`-type entry sends its key as `x-api-key`, as the Anthropic
API wants. A gateway in front of the Messages API usually wants
`Authorization: Bearer` instead: set `"auth": "bearer"` on the entry, or
export the key as `ANTHROPIC_AUTH_TOKEN`, which Claude Code reads the same
way (`ANTHROPIC_API_KEY` wins when both are set). For example, the Hero AI
broker serves the Messages API for every model it routes:

```sh
export ANTHROPIC_AUTH_TOKEN=sk-hab-v1-...
export ANTHROPIC_BASE_URL=https://api.ai02.ourhero.ai   # no /v1: the SDK adds it
janetcode --provider anthropic --model deepseek/deepseek-v4.1-flash
```

Environment: `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `OPENAI_API_KEY`,
`OPENAI_BASE_URL`, `GEMINI_API_KEY`, `GEMINI_BASE_URL`,
`ZAI_CODING_PLAN_API_KEY`, `ZAI_API_KEY`, `ZAI_CODING_PLAN` (a key may be a
comma-separated list), `JANETCODE_PROVIDER`,
`JANETCODE_MODEL`, `JANETCODE_EFFORT`, `JANETCODE_THEME`. Keys from the
environment are never copied into the config file.

Z.ai Coding Plan is available as `zai-coding-plan` in `/connect` and
`--provider`; `ZAI_CODING_PLAN` is an alias. It uses the OpenAI-compatible
`https://api.z.ai/api/coding/paas/v4` endpoint, with `glm-5` as its default
model. Set `--model` or `JANETCODE_MODEL` to choose another model.
Credentials are read from `ZAI_CODING_PLAN_API_KEY`, then `ZAI_API_KEY`,
then `ZAI_CODING_PLAN`, using the first nonempty variable. An explicit
provider `key_env` still takes precedence. Provider inference uses the
existing priority rules, so another explicitly selected provider stays selected.

OpenCode Go (`opencode-go`) and Zen (`opencode-zen`) are also presets.
Go reads `OPENCODE_GO_API_KEY`, falling back to `OPENCODE_API_KEY`.
Zen reads `OPENCODE_API_KEY`, falling back to `OPENCODE_ZEN_API_KEY`.
A generic key makes both available, with Go first during provider inference.
An explicit `key_env` overrides these default lookups. Environment keys
are never written to the config file.

A key does not have to be written in the config at all. Anywhere a
provider key is configured (`api_key`, `api_keys`, or the variable
`key_env` names), a 1Password reference such as
`op://Private/OpenRouter/credential` is accepted. janetcode runs
`op read <reference>` when it builds the provider and uses the value for
that run only: the config file keeps the reference, and saving the
config (`/connect`, `/model`) never writes the value into it. The value
is kept in memory for five minutes, so a rotated credential is picked up
on the next provider build after that. `op` must be installed and signed
in on the machine janetcode runs on; on a remote or headless machine
sign it in with a service account, since a desktop approval cannot be
answered there. If `op` is missing, locked, or the reference is wrong,
the provider fails to start with an error that names the reference.

Use `--provider opencode-go --model kimi-k3` or the equivalent
`--model opencode-go/kimi-k3`; Zen accepts the same provider/model syntax.
Both default to `kimi-k3` when no model is selected. The presets use
`https://opencode.ai/inference/go/v1` and
`https://opencode.ai/inference/v1` for the model catalog and derive the
native API URL for each model. Chat Completions, Responses, Anthropic
Messages, and Gemini models use the existing protocol clients.
Legacy `https://opencode.ai/zen/go/v1` and `/zen/v1` configurations
remain supported. `OPENCODE_GO_BASE_URL` overrides Go's URL;
`OPENCODE_ZEN_BASE_URL` or `OPENCODE_BASE_URL` overrides Zen's URL.

Every OpenCode request carries `x-opencode-session`; conversation IDs
survive resume, and children get their own IDs. Exhausted subscription
windows fail promptly with reset information when supplied by the
gateway; short rate limits retain normal retries. Models on the
proprietary `jev-*` endpoint are excluded. See the current
[OpenCode Go endpoints](https://opencode.ai/docs/console/go/) and
[model APIs](https://opencode.ai/docs/console/models/).

`JANETCODE_SHELL` names the shell that `bash`, hooks, verify commands
and background jobs run through, in place of bash on Unix and PowerShell
on Windows. It must take the command after `-c`: Git Bash
(`C:\Program Files\Git\bin\bash.exe`) and busybox-w32 (`busybox.exe`,
run as `busybox.exe sh -c`) are the Windows choices for people who would
rather write bash than PowerShell.

`effort` is how hard the model thinks: `low`, `medium`, `high`, `xhigh` or
`max`, in that order, or absent for whatever the provider does on its own.
Anything else is a config error, both in the file and through `/config`.
Anthropic, `gemini` (as the thinking level; `xhigh` and `max` ask for
`HIGH`) and `responses` (as `reasoning.effort`; `max` asks for `xhigh`)
providers send the tier; an OpenAI-compatible one remembers it without
sending it, and `/effort` says so.

It is a live setting. `/effort <tier>` changes the running session from
the next model call onward and writes nothing; `/config effort=<tier>`
does the same **and** saves it here for the next session, the way the
other live keys (`model`, `max_tokens`, `compact_at`, `auto_approve`) do.
`--effort` and `JANETCODE_EFFORT` set the tier a session starts at. A
model that refuses the tier asked for is served the one below it, and the
session shows any tier above the default on its status line.

`effort_control` (experimental, default off) lets the harness set each
call's tier from the turn itself instead of asking every call for
`effort`. A turn starts at `low`; a step that went wrong — a failed edit,
a check that came back red, a loop note, a verify gate sending the turn
back — raises it one rung; a step that made progress — a green check, the
turn's first edit to a file — halves the distance above `low`; and a
reply the plan budget or the output limit cut goes straight back to
`low`. `effort` is the ceiling (`high` when it is unset), and a turn
changes the tier at most four times. It acts only where the provider
sends a tier and its prompt cache does not key on it, which today is the
`responses` provider: on Anthropic a change of effort invalidates the
cached messages, Gemini's cache is not documented either way, and an
OpenAI-compatible server is not sent a tier at all, so there the
controller says once that it is idle and every call asks for `effort`.
The tier it chose is recorded as `effort` on each assistant event of the
session log. `/features effort-ctl on|off` and `--set
effort_control=true` turn it on for a session.

Adaptive thinking is on for Claude 4.6+ models and off for Haiku; set
`"thinking": false` to override.

`keybindings` moves the terminal interface's prompt keys, action name to
a key, a list of keys or `""` to unbind (`{"effort": "f2"}`), and
`vim_mode` (default off) starts the composer in vim mode; both are in
[the interface manual](interface.md#your-own-keys-keybindings), and
`/help keys` shows the table a session is using. A bad binding is a
config error that names the action and the key.

`ask_timeout` is how long a question from the model waits for your
answer in a turn you typed before the turn goes on with the model's
recommended choice: a duration (`90s`, `5m`), `0` to go on at once, or
`forever` (the default). `ask_timeout_unattended` is the same for a turn
nobody need be watching — a `--bg` run, a goal round, a `/loop` firing —
default `2m`. A question the model gave no recommendation for always
waits, and an answer given after the turn went on still reaches the
model. See [sessions](sessions.md#when-the-model-asks-a-question).

`lean_tools` (default on) defers the search tools behind `tool_search` —
19% less context on fan-out work, one extra call when the model needs a
search. `"lean_tools": false` exposes them all in every request.
(The measurement is the other way round: exposing the five extra tools
costs x1.23 [1.10, 1.44] context, which is a 19% saving from deferring
them, not 23%. See `docs/benchmarks/toolsurface/REPORT.md`.)

`show_thinking` (default on) renders the model's reasoning in the
terminal. Turning it off asks for reasoning without showing it, so the
first visible token arrives sooner; turn it off when you do not read
the reasoning.

`intent` (default off) asks the model for a one-line reason on every
tool call — a readable narrative at roughly a fifth more context per
call. Unlike `thinking` there is no per-request budget to weigh it
against; the reasons are read or they are not.

### Proxies and private certificate authorities

Every connection janetcode makes (the providers, `fetch_url`,
`web_search`, remote MCP servers and their OAuth logins, `--update`, the
decisions service, the price table) goes through the proxy the standard
variables name: `HTTPS_PROXY` (and `HTTP_PROXY` for plain http), with
`NO_PROXY` listing hosts to reach directly (`localhost,127.0.0.1,.corp`).
Lower-case spellings work too.

A proxy that intercepts TLS, or a gateway with a certificate from a
private CA, needs that CA trusted. Put its certificate (PEM, one or more)
in a file and name it:

```json
{ "ca_file": "~/certs/corp-root.pem" }
```

or `JANETCODE_CA_FILE=~/certs/corp-root.pem` for a run. Both may be set;
each adds to the system's roots and replaces nothing. A file that cannot
be read or holds no certificate is a configuration error (exit 52), not a
warning, because the alternative is every call failing later with a
certificate error that does not say why.

`SSL_CERT_FILE` and `SSL_CERT_DIR` are honoured on every platform. On
Linux and the BSDs they *replace* the system bundle, as they do for
OpenSSL and curl; on macOS and Windows, where Go verifies against the
system store and would otherwise ignore them, janetcode adds what they
name to the system roots.

Commands janetcode runs for you (`bash`, hooks, MCP servers started as
processes) inherit your environment unchanged and use their own trust
settings: Node reads `NODE_EXTRA_CA_CERTS`, Python's requests reads
`REQUESTS_CA_BUNDLE`, git reads `http.sslCAInfo`.

`language` is the language the model answers in and the interface is
shown in: `"language": "es"`, `"Spanish"` or `"español"` alike. The
interface's own words are catalogued in Spanish and French and fall back
to English otherwise; the model is told any language by name. Without
it, the model follows you and the interface follows `LANG`. See
[Language](interface.md#language).

`screen_reader: true` starts every terminal session in the
screen-reader line interface (see
[The screen-reader interface](interface.md#the-screen-reader-interface)).

`voice` is where `/voice` sends a recording, every field optional:

```json
"voice": {
  "model": "whisper",
  "endpoint": "https://api.openai.com/v1",
  "api_key_env": "OPENAI_API_KEY",
  "recorder": "arecord -q -f S16_LE -r 16000 -c 1 -t wav"
}
```

`model` defaults to `whisper-1`; `endpoint` to the active provider's base
URL when it speaks the OpenAI API, else `OPENAI_BASE_URL`; `api_key_env`
to that provider's key; `recorder` to the first of `arecord`, `rec` and
`ffmpeg` found. `/config voice.model=whisper` sets one field. See
[Voice input](interface.md#voice-input).

### The sandbox key

`sandbox` names the sandbox profile a session in this project starts
under, so commands are confined without anyone having to remember
`/sandbox`:

```json
{ "sandbox": "workspace" }
```

`off` (the default, and what an absent key means) starts unsandboxed;
`workspace`, `read-only` and `strict` are the profiles described in
[The sandbox](interface.md#the-sandbox), which also lists what each
backend — docker, macOS seatbelt, Linux Landlock — can and cannot
enforce. A value that is not one of those is a config error, not a
session that quietly is not sandboxed.

`--sandbox` on the command line turns one on without naming a profile
and means `workspace`; a config file that names another profile keeps
it. `/sandbox <profile>` changes it for one session, and `/config
sandbox=strict` changes the project default and what a running sandbox
enforces — it does not turn a sandbox on, because a backend can fail and
`/sandbox` is where you would want to hear about it.

### The protected_paths key

Some files are not work: a shell startup file, a git hook, an `.npmrc`,
a key under `.ssh`. A write to one of those changes what happens the
next time you open a terminal, commit, publish or connect — long after
the session ended. They are protected: writing one always asks, under
`/edits`, `/auto`, a saved allow rule and `--yolo` alike, and the prompt
says which path it is asking about ("protected path: ~/.zshenv"). See
[Protected paths](interface.md#protected-paths) for the whole set and
the reasoning.

`protected_paths` adds to that set:

```json
{ "protected_paths": ["~/work/deploy/**", "/srv/secrets.env"] }
```

Entries are **appended** to the built-in list and never replace it, so
this key can only make a session ask about more, never about less. A
pattern is matched the way a permission rule is — `*` spans anything,
including `/` — a leading `~` is your home directory, and a pattern
ending in `/*` or `/**` covers the directory itself as well as what is
in it.

### Ignore files: `.aiignore`

A project can tell the agent which files to stay out of with an
`.aiignore` (also read: `.agentignore`, `.janetcodeignore`), in
`.gitignore` syntax. Files in subdirectories apply to their own subtree,
`!` re-includes, a trailing `/` means a directory, and an excluded
directory excludes everything under it:

```gitignore
# customer data and credentials
/fixtures/customers/
*.pem
.env*
!.env.example
```

`read_file`, `write_file`, `edit_file`, `list_dir`, `glob`, `grep` and
`read_many_files` honour it. A listing or search leaves excluded paths
out, and naming one is refused with the file and line that excluded it
(`secrets/token.txt is excluded ... by .aiignore:2`). Edits to the file
apply within a second, without restarting.

This is a statement of intent, not a sandbox. `bash` can still `cat` an
excluded file, the same way `.gitignore` does not stop anyone reading
one. To keep a file from a session for certain, use `--sandbox` or keep
the file outside the project. `.gitignore` itself is not consulted:
build output and vendored code are often exactly what a question is
about. Over `--ssh` and `--docker`, the remote side's ignore files are
not read.

### Managed policy

An organisation can install a policy that every session on a machine
runs under and nobody inside a session can change: not `/config`, not
`--set` or another flag, not the model, and not an edit to the user's
own config file, because the policy is applied after all of them.

It is one JSON file at a path only an administrator can write:

| OS | path |
|---|---|
| Linux and the rest | `/etc/janetcode/managed.json` |
| macOS | `/Library/Application Support/janetcode/managed.json` |
| Windows | `%ProgramData%\janetcode\managed.json` |

```json
{
  "message": "Managed by ACME IT, see go/janetcode",
  "settings": { "effort": "high", "memory": false, "search": {"provider": "brave", "key": "${BRAVE_API_KEY}"} },
  "permissions": {
    "allow": ["bash(cmd:go test *)"],
    "deny":  ["*(host:*.internal.example)", "bash(cmd:git push * --force*)"],
    "ask":   ["*(touches:**/.env)", "*(secret:*)"]
  },
  "managed_permission_rules_only": false,
  "disabled_tools": ["web_search", "mcp__*__delete*"],
  "allowed_providers": ["corp-gateway"],
  "allowed_models": ["claude-*", "qwen3*"],
  "sandbox": "workspace",
  "disabled_modes": ["yolo"],
  "allowed_mcp_servers": ["github", "jira"]
}
```

Every key is optional:

- `settings` fixes config keys to a value, in the config file's own
  shape. They override the file, the environment and the command line,
  and `/config` and `--set` refuse to change them ("changing effort is
  not allowed by the managed policy (/etc/janetcode/managed.json): …").
- `permissions` are rules that always apply, in the shape of the
  config's own (see [Permission rules](interface.md#permission-rules)).
  Their deny and ask rules outrank every allow rule, as any deny does.
  With `managed_permission_rules_only`, the policy's allow rules are the
  only ones: the user's saved rules, the project's and `--allow` are
  dropped, `/permissions allow` is refused, and `r` at a prompt allows
  just that call.
- `disabled_tools` are tool names, with wildcards, the model is never
  offered — including MCP tools that come up later. Calling one gets
  "tool is withheld by policy (disabled by the managed policy)" rather
  than "unknown tool", so the model does not hunt for a workaround.
- `allowed_providers` (by configured name or by type, `anthropic` or
  `openai`) and `allowed_models` (wildcards) are the only ones a session
  may start on or switch to with `/model` or `/connect`.
- `sandbox` turns that sandbox profile on for every session, and
  `/sandbox off` or another profile is refused.
- `disabled_modes` are the approval shortcuts that cannot be turned on:
  `yolo` (`--yolo`, `/yolo`, `auto_approve`), `auto` (`--auto`, `/auto`)
  and `edits` (`--auto-edits`, `/edits`). A flag asking for one is a
  usage error (exit 42); `auto_approve: true` in the user's file is
  switched off with a note.
- `allowed_mcp_servers` are the only MCP servers that may start, from
  the config or a project's `.mcp.json`; the rest are listed by `/mcp`
  with the reason. An empty list allows none.

A file that exists and cannot be read or parsed stops janetcode from
starting (exit 52): a policy that silently stopped applying would be
worse. `JANETCODE_MANAGED_FILE` names one more policy file, for tests and
container images; it is read *after* the system file and can only make
the policy stricter — its disallowed things are added, its allow-lists
are intersected with the system file's, and a setting, sandbox or
message the system file fixes keeps the system file's value.

`janetcode --inspect managed` prints the policy in force and where it
came from, `/permissions` lists what it fixes under "managed", and
`--doctor` reports it.

### What janetcode records about you

Two small files in the config directory, and nothing else:

- `hints/<project>.json` — the ids of the hints already given for one
  project, so each is given once.
- `usage.json` — a map from feature name to how often you have used it,
  written once when a session ends. It is what lets the hints skip the
  features you already know and `/stats` name up to five you have never
  tried.

Neither leaves the machine. There is no endpoint, no identifier and no
timestamp; nothing in janetcode reads them except the hint rules and
`/stats`, and deleting them costs nothing but a few repeated hints. The
record of *what you did* — the conversations, the tool calls, the costs —
is the session log, which is also local and which `/history` and
`/resume` read.

### Hints

`"advisor_model": "z-ai/glm-5.3"` is who the deferred `advisor` tool
consults (resolved through the same provider routing as `/model`). Empty
uses the session model. Setting it also activates the `advisor` tool for
that session. A cheap executor plus a stronger advisor looks like
`"model": "inception/mercury-2.5"` with that `advisor_model`.

### The forced advisor

`"advisor_mode": "always"` makes the harness buy a consult with
`advisor_model` at the start of a turn and again the first time the
verification gate fails, and injects the reply into the executor's
context as guidance text. The advisor has no tools and does not act.

```json
{
  "model": "mistralai/devstral-2512",
  "advisor_model": "qwen/qwen3.7-plus",
  "advisor_mode": "always",
  "advisor_max_consults": 2
}
```

The default is `"off"`, which leaves the `advisor` tool exactly as it was:
available when `advisor_model` is set, and pressed only if the model
decides to. That distinction is the point. A consult offered as a tool is
a consult the model declines -- in the mixai measurements the strategies
that put the panel behind a tool call were never pressed and scored
identically to the cheap model working alone. So `always` takes the
choice away from the model.

`advisor_max_consults` caps consults per turn (default 2). Every failure
path is open: a spent budget, a provider error, a timeout and an empty
reply all leave the turn exactly as it would have been with the feature
off. `--eval-off advisor` switches it off for one run.

A turn-start reply that hit the token limit, or that exists only as the
model's thinking, is dropped rather than injected: half a plan anchors
the executor on its first half.

#### Orient instead of plan: `advisor_turn_start_style`

`"advisor_turn_start_style": "orient"` changes what the consult at the
start of a turn asks for. Instead of "give it the plan", a model reads the
request, the project's check commands, the current contents of the files
the repository map ranks highest for the request (up to four, 16 KB) and
what sits beside them, and answers in three sections: where to LOOK, what
to WATCH FOR, how to CHECK. It is told not to say what to change. Its
answer goes into the context as evidence about where to look, not as
instructions.

Orient runs on every turn whether or not `advisor_mode` is `always`, on
`advisor_model`, else `strong_model`, else the session's own model, so a
cheap model can orient itself. With `advisor_mode` `always` it is that
turn-start consult and counts against `advisor_max_consults`. `plan` (the
default) keeps the original consult. `--eval-off orient` switches orient
off for one run.

Why it exists: hero_agent measured the two shapes against each other and
found that prescribing an edit before anyone read the code can turn a
pass into a fail, while pointing at evidence helped. It is off until
janetcode's own A/B says so (`docs/benchmarks/orient-baseline/PLAN.md`).

### The strong slot and the cascade

`route_cheap`, `model` and `strong_model` are three of the model slots a
session has: the cheap one a mechanical step may dip to, the default one
it runs on, and the one a failing turn escalates to. They are slots, not
vendors -- which model fills which is a configuration question, and the
answer changes with the month. The full set is in "The model slots"
below, together with the one rule that governs all of them: an empty slot
is the session's own model.

```json
{
  "model": "mistralai/devstral-2512",
  "strong_model": "qwen/qwen3.7-plus",
  "escalate_after": 1
}
```

`"escalate_after": N` turns on the cascade: when the verification gate
(see `.janetcode/verify.json`) has failed `N` rounds in a turn, the rest
of that turn's model calls go to `strong_model`. The next request starts
on the default model again.

Both halves are required and both default to off, so a configuration that
names neither behaves exactly as before. `--eval-off escalate` and
`JANETCODE_OFF=escalate` switch it off for one run.

Two things about the trigger are deliberate. It is the real check
failing, not a judgement about how hard the request sounds: a model asked
to predict difficulty routes on the wording, pays the strong rate for
work the cheap model could do, and still misses the one task that needed
it. And the decision provider's failure triage (`decisions.triage`) runs
*before* it, so a failure it confidently calls environmental or flaky
ends the turn without buying a strong-model round -- that veto costs no
extra call, it is just the ordering.

There is a second trigger, with no key of its own: a turn that has spent
60% of its `max_iterations` and is still going escalates too. It exists
because of what the gate cannot see. When the model can run the grading
tests itself, it only ends a turn once they pass, so the gate agrees with
it and never fails -- it confirms rather than catches. Burning most of the
budget is still a fact about this run rather than a prediction about the
request, which is the property the cascade is built on.

Escalation ignores the 20k cross-provider context ceiling that guards
cheap step routing. That ceiling exists because a cold prompt cache is
not worth paying for a *cheaper* answer; when the alternative is a turn
that fails its own gate, it is. Expect a cache miss at the switch.

`"computer_use": true` starts Cua Driver as an MCP server when
`cua-driver` or `open-computer-use` is on PATH. Off by default. `/cua on`
sets it and starts the server in the running session.

`"followup": true` turns on ghost-text next-prompt suggestions after a
turn. They are off by default. A cheap side query (no system prompt, no
tools) proposes 2–12 words; Tab accepts them in the terminal. `/followup
on|off` toggles the running session; `/config followup=true` saves it.

`"recap_idle_minutes": N` writes a `/recap` into the terminal transcript on
the first keypress after N minutes without one, when the conversation has
grown since the last recap. 0 (the default) is off. `/config
recap_idle_minutes=30` saves it.

`"hints": false` turns off the one-time notes about features a session
could have used. They are on by default, and there are nine of them: the
verification gate after the third changing turn or the second time you run
the tests yourself with `!`, the `r` key after the third refusal of the
same shape of call, `ctrl+g` after a command holds a turn for twenty
seconds, `/context` and `/compact` when the window crosses 60%, `@path`
after a large paste or a pasted stack trace, `/usage` when a session
crosses $0.50, the trust command for a project file that is present but
not enabled, the `prices` key for a model nothing can price, and
`/context` on a session resumed after a compaction.

Each fires at most once per project, ever, and never while you are
answering a permission prompt or reading a picker. The record of what has
been said is a small JSON file under the config directory, keyed by
project — never in the project itself. `/hints off` silences them for one
session, `/hints on` brings them back, and `/hints` alone says how many
have been given.

### Release notice

`"update_check": false` stops the TUI saying when a newer release is out.
On by default, it asks GitHub for the latest release tag at most once a
day (the answer is cached in `update-check.json` in the config
directory), in the background with a five-second limit, and a failed ask
waits for the next day too. Headless runs and builds that are not a
release never ask. `janetcode --update` installs the new release.

### Proof notes

`"proof_notes": false` turns off attaching evidence to a commit this
session makes. On by default: when a bash call runs something that looks
like `git commit` and HEAD actually moves, janetcode attaches a git note
recording which turns of this session wrote which of the commit's files,
their stated intent, and what the harness actually observed demonstrating
the work (see "Proof in the commit" in [Interface](interface.md)). It
costs one `git notes add` per commit this session makes and nothing
otherwise, so unlike `intent` there is no per-request budget to weigh it
against — the setting exists for a repository where a third-party git
notes ref is unwelcome, or a CI checkout where the write would fail
anyway. `/config proof_notes=false` sets it for one session; `/proof`
still attaches on request regardless, since that is something you asked
for directly. The signed evidence attached beside each note (see
"Signed evidence" in [Interface](interface.md)) rides on the same
setting; `JANETCODE_OFF=evidence` keeps the notes and drops the evidence.

### Model fallback

`fallback_models` is an ordered list (at most three are used, duplicates and
the model in use dropped) tried when a call fails in a way another model
could survive: an exhausted quota or plan, rate limits and server errors
that outlived the provider's own retries, or an endpoint that could not be
reached at all (connection refused or reset, a name that does not resolve,
a stream that stalled or was cut off on every attempt). The decision reads
the HTTP status, the error type a server sends inside an open stream
(`overloaded_error`, `insufficient_quota`, OpenRouter's numeric `code`) and
the transport error itself; only a provider message with none of those is
judged by its words. A bad request, a context overflow
or a cancellation fails as it always did — another model would fail the same
way. Each name is routed like any other, so a fallback may live on a
different provider. The partial answer from the failed attempt is discarded,
the conversation is resent whole, and the model that works becomes the
session's model (`/model` switches back).

```json
{ "model": "qwen3.7-plus", "fallback_models": ["glm-5", "claude-opus-5"] }
```

### Step routing (experimental)

`stale_check_nudge` (default off) covers the one premature stop the
verdict rules cannot see. A turn that edits, runs the tests, edits again
and then says it is done has every total green — something ran, it
passed, tests were exercised — and none of it was run against the tree
as it now stands. With the key on, such a turn is sent back once, with
the evidence stated, sharing the same one-correction-a-turn budget as
the overclaim gate: the expensive mistake here is nagging a finished
turn into another round of model calls. A turn that never ran anything
is already `unverified` and already sent back, and is not nudged twice.

`conflict_exit` (default off) offers the model a way out of a task
whose tests contradict it: one prompt line, and a `CONFLICT: <test id> |
<the task's words> | <why>` line in its final answer. The harness checks
the claim rather than taking it: the cited test must exist in the
project and fail when the turn ends. A confirmed conflict ends the turn
without the verify gate and the reviews that would send it back to make
that test pass, is recorded in the proof note, and ends a `-p` run with
exit 58; an unconfirmed one is sent back once. Off by default because it
changes the prompt, and a model that reads it as an escape hatch from a
hard task costs passes (measure with
`docs/benchmarks/effbench/impossible`).

`subagent_merge` (default off) has the harness merge an editing
subagent's work (`task` with `edit: true`) into the project when it
finishes, instead of handing the patch to the model to `git apply`. The
subagent's read-set — the files it read, the greps and globs it ran — is
checked against what changed in the project meanwhile; edits that touch
different declarations than the concurrent change are merged with no
model call, and what collides is sent back to the subagent for one
repair round with both sides' diffs. It is off by default because it
writes to the project without the main model applying anything, and
because the repair round is a model call; it never writes where a write
would need your approval. See "Editing subagents in a worktree" in
[the interface manual](interface.md).

`plan_file` (default off) makes `.janetcode/plan.md` a live plan. Edit
it in your own editor while the agent works: before each model call the
harness compares it with what it last saw and sends the model only the
change, as a diff with a line of context, marked as the harness's. The
first look in a session sends the plan whole; deleting the file is said;
blank-line and trailing-space edits are not changes; the agent's own
edits to the file are not sent back to it as yours. It is off by default
because it adds text to the conversation, and a plan file that came
with a cloned repository is not one you wrote.

`probe_ledger` (default off) keeps the inline checks the model runs
after it edits — a `python3 - <<'EOF'` script with asserts, a `node -e`
that throws — instead of letting them vanish. At the end of a turn that
changed something, each new one that still passes is run again against
the tree as it was before the turn's first change (exported from the undo
snapshot into a temporary directory). One that fails there and passes now
tells the change apart from the old code: it is frozen in harness state,
where no edit can reach it, and replayed at every later gate; when a later
edit breaks it the turn goes back with its output, at most twice a turn,
and one left failing into the next turn is dropped as declined. One that
passes on the old code too is vacuous, and the spec review is told so. A
probe is rerun through the bash tool, under the session's sandbox, and
only when the permission rules would let that call run without asking.
`JANETCODE_OFF=probes` turns it off.

`probe_memory` (default off, and it turns `probe_ledger` on with it)
keeps the ledger's discriminating probes beyond the session, in the
repository under `refs/janetcode/probes` — a ref that is never checked
out and never moved by a plain `git push` or `git fetch`. Each entry is
the command with the workspace path taken out, the intent of the change
it told apart and the session that made it. The first gate of a later
session arms up to eight of them, newest first, but only those that pass
on the tree as it was before that turn's first change: a remembered probe
that already fails there says nothing about the turn and is left out.
The armed ones are replayed like the ledger's own; one the turn breaks
sends it back with "remembered from an earlier session …, which made it
pass: <intent>", and one the model leaves failing into the next turn is
retired from the memory as well. It runs through the bash tool under the
same sandbox and permission rules as the ledger. `janetcode --session-ref
probes` lists the memory, `--session-ref push` / `fetch` move it (a fetch
merges the remote's probes into yours). `JANETCODE_OFF=probememory`
turns it off.

`read_blame` (default off) adds a note to a `read_file` result when the
lines shown include a range an earlier agent turn wrote for a stated
reason: `L12–19 turn 2 (#5, 2026-10-01): guard a non-positive page size ·
checked by go test ./...` for this session's own changes, `L12–19 commit
1a2b3c4 (verified): …` for lines a committed session wrote (from its proof
note). Only ranges of two lines or more with an intent are noted, never
the running turn's own, never an undone change or a file edited outside
the session since; at most four ranges and 480 bytes a read. It changes
what the model reads, so it is opt-in; `JANETCODE_OFF=readblame` turns it
off.

When `grep` finds nothing (or a `grep`, `egrep`, `fgrep`, `rg` or `git grep`
run through `bash` does: a single plain command, optionally piped once into
`head` or `tail`, with no other shell syntax, and not sandboxed or remote;
the note follows the command's output as `[empty search probe] ...`) it tries, in order, the same pattern ignoring
case, the pattern as a literal string (when it has regex metacharacters),
and the search without its glob, and stops at the first that finds
lines. The result stays "No matches." and adds what would have matched:
the count (capped at 200+) and up to three example lines. Probes are
read-only, each stops at 200 matching lines, and a session runs at most
20 of them. `JANETCODE_OFF=emptyprobe` turns it off.

`numeric_claims_nudge` (default off) sends a turn back once when its
answer states a number that no measurement in the session backs. The
check itself is always on and only reports (see "Numbers in the answer"
in [the sessions manual](sessions.md)); this key adds the send-back,
which asks the model to measure the number — before and after, for a
speed or memory change — or to say it is an estimate. It shares the
verdict's one correction a turn.

`diff_fuzz` (default 0, off) is how many seconds to fuzz a refactor. When
the request (or a `BRIEF.md`/`TASK.md` at the project root) says the
change keeps behaviour the same — "refactor", "optimise", "clean up" with
nothing new asked for beside it, or "without changing behaviour" in so
many words — a turn that changed Go code is, before it ends, compared
with the code as it was before the turn: the module is copied to a
temporary directory, the package as it was is written beside the new one
under an alias import path, and a generated `testing.F` harness calls
each changed exported function (and each that calls one) on both with
the same inputs, seeded with the literals the package and its tests hold.
The first input they disagree on sends the turn back once, naming the
call and both results. Functions that disagree with themselves on an
input (time, randomness) skip it. Methods, generic functions and
parameters of the package's own types are not compared yet; nothing is
written to the project, and the build runs with `GOPROXY=off`. It runs
outside the session's sandbox, like the test ledger's replay.

```json
{ "diff_fuzz": 20, "numeric_claims_nudge": true }
```

`scope_justify` (default off) asks for a reason when an edit leaves the
task's scope. The scope itself is always inferred (see "Scope contracts"
in [Sessions](sessions.md#scope-contracts)); with this key on, the
result of an out-of-scope edit that came with no stated intent gets one
line asking the model why the change is needed, and its answer is
recorded as that edit's justification. It is off by default because it
adds a line to what the model sees.

`preresolve` (default off) looks up what a fresh session's first message
names before the model sees it, in a repository too big to be sent whole
(over 40 files or 32 KB). A path that is an indexed file, or the tail of
exactly one, is sent whole when it is 6 KB or less (and counts as read),
else as an outline of its definitions. A backticked name, or a code-shaped
one in the prose (`parseConfig`, `max_retries`, `Cache.Get`), defined in
at most three files, is sent as its definition's excerpt with the comment
above it — Go measured by its own parser, Python by indentation, the rest
by braces, at most 60 lines — followed by up to two test functions that
mention it. A plain word counts only when it is part of exactly one
compound definition name in the whole index ("refund" for
`RefundWindowDays`). Everything goes in one `<resolved>` block of at most
12 KB, after the repository map. Nothing runs: no model, no command, no
language server.

`route_cheap` is a model name; empty (the default) is off. When set,
eligible continuations can use it for repository reads: `read_file`,
`list_dir`, `glob`, `grep`, and `repo_map`. The cheap model receives only
those tool definitions. If it asks for another tool anyway, including a
tool call written as prose, that reply is discarded and the session's
model handles the step. The discarded call's usage is still billed.
It is resolved the
same way `fallback_models` is, so it may live on a different provider;
a name nothing can route turns the feature off for the session with one
notice rather than failing every call.

```json
{ "model": "claude-opus-5", "route_cheap": "claude-haiku-4.5" }
```

The policy is deliberately not learned — see
`internal/agent/steproute.go` for the exact rules — but the short form is:
a step routes only when it is not the turn's first call, the step right
before it was a clean batch of nothing but reads and passing checks, and
nothing has yet happened this turn that means "pay attention" (a
verification or acceptance check failing, the trajectory critic warning,
a loop nudge, or a note you typed mid-turn). Any of those sends the very
next call back to the session's model and keeps every later call in the
turn there too.

Switching models mid-turn breaks the provider's prompt cache: the whole
prefix is billed as fresh input on the call that switches, in either
direction (see "Prompt-cache accounting" via `/why` or the cache report,
and `internal/agent/cachestat.go`). So the router only ever switches when
that cost is small: `route_cheap` is on the same provider as the
session's model, or the conversation so far is
small enough — under 20,000 estimated tokens — that repricing it from
cold costs little in absolute terms even across providers. Past that, a
cross-provider route is skipped for that call.

This is off by default because its value has to come from an eval run,
not from turning it on and reading the transcript: see "Measuring step
routing" in [Evals](evals.md) for the exact commands and what result
would justify leaving it on. `/stats` reports "routed N of M model calls
to `<cheap>`" once anything has actually routed, and `/config
route_cheap=` (empty) turns it back off for the running session.

Every model call records which slot served it and why. The assistant
events in the session log carry `slot`, `provider`, `route` and
`route_why` (a main call is `slot: "main"` with an empty `route` — a
slot that resolves to the session's own model is reported as no route),
and the trajectory ledger marks a routed call `↪ cheap` on its line with
the policy's reason in the inspector. `/routing` vetoes the router for
the rest of the session: while it is off, every call goes to the
session's own model no matter what `route_cheap` names.

### The model slots

A session runs on one model and may name others for particular jobs:

| slot | key | what fills it does |
|------|-----|--------------------|
| main | `model` | every call that no other slot claims |
| cheap | `route_cheap` | a turn's mechanical steps, when step routing fires |
| strong | `strong_model` | the rest of a turn whose gate kept failing, with `escalate_after` |
| advisor | `advisor_model` | the advisor tool's consults, forced when `advisor_mode` is `always` |
| vision | `vision_model` | reads the images a model that cannot see would be sent, and describes them for it (below) |

**Every empty slot is the main model.** One resolver says so --
`(*config.Config).Slot(name) -> (provider, model)` in
`internal/config/slot.go` -- and a slot set to the same name as `model`
resolves identically to an unset one, so the two cannot behave
differently. The provider is whatever `RouteFor` names for that model, so
a slot may live on another configured provider exactly as a fallback may.

What the resolver deliberately does not answer is whether a feature is
**on**. Filling the strong slot does not start the cascade (that is
`escalate_after`), filling the advisor slot does not force consults (that
is `advisor_mode`), and naming the cheap slot is what turns step routing
on at all. `/features` prints both halves together for that reason: a
filled strong slot with the cascade off says so rather than looking like
a working cascade.

The cheap slot has one special case the fallback makes worth stating:
when it resolves to the session's own model, no call is routed. Routing a
call from a model to itself changes nothing, and counting it as a route
would put a line in `/stats` about work that never happened.

From the terminal, `/model` is every slot: `/model` alone (or `/model
<name>`) is the session's own model as before, `/model cheap`, `/model
strong`, `/model advisor` and `/model vision` open the same searchable
provider list for the slot named, and `/model <slot> off` empties one.
An unset slot reads `cheap: unset (main: anthropic/claude-opus-5)`.

### Images for a model that cannot see: `vision_model`

Many coding models read no images: DeepSeek, Qwen Coder, most GLM
releases. Setting `vision_model` says the session's model is one of
them. From then on, every image a request would carry to any other
model — a screenshot attached with `@file`, a paste, an image an MCP
tool returned — goes to the vision model first, and what it says the
image shows is sent in its place: every piece of text transcribed
exactly, the layout and state of what is on screen, a chart's values.
The text the image came with tells it what to look at. The session's
model reads the description marked as coming from the vision model, and
as data rather than instructions.

- An image is described once per session: every later request that
  carries it reuses the description, so a screenshot early in a long
  session is not paid for on every call.
- The session keeps the image itself. Switching the session to the
  vision model (or to any model named by `vision_model`) sends the real
  image again, and so does emptying the slot.
- If the vision model fails, the session's model is told an image was
  there that could not be described and to ask you if it matters; the
  raw image is never sent to a model declared unable to read it. It is
  tried again next turn. A name that cannot be routed turns the bridge
  off for the session with one notice, and images then go to the
  session's model as they would without it.
- The vision call is billed with the session's other calls, under the
  vision model's name in `/usage`.

`vision_model` names a model the way the other slots do, on the session's
provider or any other configured one; `/model vision` picks one from the
list. Leave it empty when the session's model reads images itself.

### Turning the features on from the session

Every key above is settable while a session runs, through `/config
key=value` or through the two commands that group them:

- `/features` lists the slots and the switches that use them --
  `escalate` (`escalate_after`), `advisor` (`advisor_mode`),
  `stale-nudge` (`stale_check_nudge`), `effort-ctl` (`effort_control`),
  `localize` (`localize_failures`), `subagent-merge` (`subagent_merge`),
  `plan-file` (`plan_file`) and the four decision consumers --
  with what each is set to and what still stands in its way.
  `/features <name> on|off` changes one.
- `/jev` is the decisions block: see below.

All of them are written to the config file and applied to the running
session. A headless run has no session to type in, so `--set key=value`
(repeatable) takes the same keys, for that run only and without touching
the file: `janetcode -p '...' --set escalate_after=1 --set
decisions.triage=true`. A key or value the file would be refused for is
refused there too.

### Decisions (experimental, optional)

The `decisions` block configures an optional second AI service —
TypeSafe's Jev, a structured-decision model that answers typed questions
with probabilities instead of writing text. janetcode uses it only for
narrow advisory judgments around the loop; the first is **verification
triage**: after a verify-gate failure, the failure (command, bounded
output, the turn's changed files, your request) is sent to the service
and classified as patch-caused, environmental, flaky, unrelated or
inconclusive. A confident environmental/flaky/unrelated verdict ends the
turn without spending a repair round the model would almost certainly
lose; anything else behaves exactly as before. The exit code stays
authoritative — the service never reinterprets a check's result, cannot
permit, refuse or edit anything, and is never consulted by the
permission system or the shell-safety classifier.

```json
{
  "decisions": {
    "model": "~typesafe/jev-latest",
    "api_key_env": "OPENROUTER_API_KEY",
    "timeout_ms": 800,
    "triage": true,
    "intake": false,
    "intake_route": false
  }
}
```

The default endpoint is OpenRouter's decisions route
(`https://openrouter.ai`, verified live against `~typesafe/jev-latest`,
whose responses always report the pinned version); point `base_url` at
`https://api.typesafe.ai` for the native API and its `TYPESAFE_API_KEY`.

`model` must be a pinned version id or a `~typesafe/jev-*` slug; bare
aliases like `jev-latest` are refused at load so a threshold cannot
silently retune itself when the alias moves -- and refused by `/jev
model` and `--set decisions.model=` as well, so a file that will not load
is never written in the first place.

`"enabled": false` is the master switch `/jev off` writes: the block
stays, the model and the consumers stay, and nothing is asked. A block
with no `enabled` key is on, which is what every block written before the
switch existed means.

`/jev` is the whole block from the session: alone it reports whether it
is on, which endpoint and pinned model it asks, which consumers are on
and what the calls have cost so far; `/jev on|off` is the master switch;
`/jev model` and `/jev provider` open a list; and `/jev
triage|intake|route|outcome on|off` toggles one consumer. The client is
rebuilt from the config on every change, so all of it takes effect in the
session that typed it rather than the next one -- `/jev` says so, and
says instead what is missing when there is no client (no key in the
environment, or nothing switched on).

The provider list is a list with one entry today: OpenRouter's decisions
route, the one whose wire contract is verified live. TypeSafe's native
host is still reached by setting `base_url` by hand. The model list is
the `~typesafe/jev-latest` slug plus whatever you type, because nothing
enumerates the service's versions: the endpoint has no model listing and
the chat provider's `/api/v1/models` does not carry them.

`JANETCODE_OFF=decisions` is above all of it. No command here can undo
it, and `/jev` says so when it is set: an ablation a session could
switch back on is not an ablation. The key is read from the
environment named by `api_key_env` and never lives in the file. Each
consumer is off until its own key is set; the block being present alone
enables nothing.

Three consumers exist. `triage` is the one above. `intake` classifies the
session's **first prompt** — what kind of request it is (explain /
implement / debug / review / docs / other), how much work it looks like
on a five-rung rubric, and whether it would go better with an explicit
plan — and then does nothing with the answer: it is written to the
session log as a `decision` event marked `"acted_on": false`, counted in
`/stats`, and read by nobody. Routing, plan mode and the todo hint keep
their deterministic rules. The call is made on its own goroutine beside
the turn's preparation and collected once the turn's work is done, so it
adds no latency to the turn; an answer that is somehow still in flight
by then is dropped rather than waited for. It exists to produce the
calibration data that acting on it would first have to earn.

`intake_route` is the one thing that acts on that classification, and it
needs both `intake` (there is otherwise nothing to read) and
`route_cheap` (there is otherwise nowhere to route).
When the classifier is confident the first prompt is an **explain** of
low complexity that wants no plan, the turn's first model call goes to
`route_cheap` instead of the session's model — which the step router
otherwise refuses, because before the first step it has no finished step
to judge. Everything else about step routing still applies from the
second call on, so the first edit, the first failure or the first
warning ends the route for the rest of the turn.

Turning this on is the one case where the intake call costs the turn
something: routing has to be decided before the first request, so the
turn waits for the classification, at most 700 ms and only for whatever
is left after its own preparation. A late answer is still collected,
priced and logged at the end of the turn — it simply does not route, and
the turn runs exactly as it would with the consumer off. The record then
carries `"route": "cheap"` or `"default"`, the reason, and `"acted_on"`.

The gate is deliberately lopsided, because the expensive mistake is
sending hard work to the weak model and the cheap one is only a saving
foregone. `review` asks for no change either and is still excluded: it
is the kind the classifier cannot size, scoring "tell me whether the
private-address guard can be bypassed" at 0.23 on the rubric. The
measurements are in `docs/notes/jev.md`.

What leaves the machine when triage fires: your request, the failing
command and its captured output, the changed files with line counts, no
diffs; when intake fires, your first prompt and nothing else. The state
is scanned for credential-shaped text on its way out and each match —
not the field around it — is replaced with `[redacted]`, so a key in a
failing command stays home while the evidence beside it still reaches
the service. Calls are bounded by `timeout_ms` (default 800, at most
2000) and never retried. Three consecutive errors — or a median past one
second over a window of ten calls — pause the feature with one notice;
five minutes later a single probe call is allowed, and it either closes
the circuit or buys another five minutes. A dead or slow decision
service therefore costs a session one call per five minutes, and a
service that recovers mid-session is used again. `JANETCODE_OFF=decisions` (or `--eval-off decisions`) disables
the calls regardless of the config, and `/stats` gains a
"decisions: N calls · M errors · avg Xms · $X" line once anything has
been asked — the cost being the service's own reported figure, never a
local estimate — plus what intake made of the first prompt when that
consumer is on. The headless JSON envelope carries the same under
`decisions`.
`janetcode --doctor` probes the endpoint when a key is present. The
design and the measurement gates are in
`docs/notes/jev-integration-spec.md`.

Calibration is explicit:
`janetcode --decisions-replay <corpus.jsonl>` scores a labeled state
corpus (one JSON line each: `name`, `truth`, `state`) through the same
question set and action mapping a live turn takes, writes
`<corpus>.answers.jsonl` beside it, and prints the per-class table —
skip rate, mean caused probability, correct vs costly-wrong-skips.
`docs/benchmarks/decisions/triage.jsonl` holds the shipped corpus.
The spec's gate: ≥70% correct overall and no wrong skip where the truth
says the patch caused the failure.

A corpus names the consumer it is labeled for in its rows
(`"consumer": "intake"`); the triage corpus, which predates the field,
leaves it out. `docs/benchmarks/decisions/intake.jsonl` is the intake
corpus, scored on the `kind` answer and, where a row carries a
`truth_route` label, on the routing mapping `intake_route` acts on: the
confusion matrix names every costly wrong route by row.

`janetcode --decisions-annotate <sessions>` is the offline labelling
run. It takes a session `.jsonl` or a directory of them, asks the
decisions model four questions about each recorded turn — how it ended
(success / partial / failed / abandoned), whether it made progress,
whether work was wasted, whether a tool was misused — and writes two
files into `--decisions-annotate-out` (the working directory by
default):

- `annotations.jsonl`, one `agent.Annotation` row per turn: session id,
  turn, the four answers with the outcome's confidence, the call and
  failure counts, and the harness's own verdict line for comparison.
- `critic-train.json`, shaped like an eval result file so `--eval-train`
  reads it unchanged: one sample per session, the trajectory feature
  vector an eval run would have recorded, labelled by how the last turn
  ended. This is the only way the local critic gets a negative class
  without a failing eval run.

Nothing live reads either file, and nothing in a session calls this: the
sessions are already over. The rows are model annotations, not ground
truth — `docs/notes/jev.md` records what they agree with the harness's
by-construction waste labels about.

### Profiling a recorded run

`janetcode --session-profile <path>` is the offline counterpart with no
model in it at all. It reads recorded runs — an `--eval-json` result
file, a session `.jsonl`, or a directory of either — and prints, per
model: how many calls were exploration, edits, verification or none of
those; how many model calls came before the first edit; how the input
grew per call; and how much of the input is tool output older than N
model calls that nothing ever referenced again. `bash` is classified by
its command, with `shell.IsCheck` deciding verification.

`--session-profile-stale N` (default 6, the tail compaction keeps
verbatim) sets that age. "Never referenced again" is by construction and
means both things: the call's subject — path, pattern or command — is
never touched by a later call, and no line of its output over 24
characters is quoted in any later assistant message.

The point of the report is to bound a context-curation change before
building it: exploration share is the ceiling on what pre-selection can
save, and the stale share is the ceiling on what pruning could recover.

`docs/notes/jev.md` records the measured ceilings.

### Stream watchdog

A streamed model call can stop producing without ever failing: the socket
stays open and the turn waits forever. Two bounds end it. `stream_idle_timeout`
(seconds, default 240) is how long the stream may produce nothing; every
increment resets it, so a slow model that keeps talking is never cut off.
`stream_max_duration` (seconds, default 900) is the longest one call may
take however steadily it streams, and nothing resets it. Either one ends
the turn with a normal failure you can resend; set a bound negative to
disable it.

```json
{ "stream_idle_timeout": 240, "stream_max_duration": 900 }
```

### The model fingerprint (experimental)

An opt-in check that the route serves the model it names. It is off by
default, because it sends requests you did not ask for.

`fingerprint` sends six canary prompts with known answers at the start of a
session's first turn (who trained you, a multiplication, a reversed word, a
letter count, a capital, a sequence) and compares the replies with the
fingerprint stored for that provider and model under
`~/.local/share/janetcode/fingerprints/`. The first session on a route
records the reference. A model that names a maker other than the requested
model's vendor, gets two canaries wrong that it used to get right, or
changes the shape of most of its replies is reported as a notice; the turn
goes on. A mismatching fingerprint is never stored over the reference;
delete the file to accept a change.

```json
{ "fingerprint": true }
```

### Hook limits

A hook is a shell command on the critical path of a tool call whose output
is pasted into the conversation, so what hooks may cost is bounded, and the
bounds are yours to set. They live in the `"hooks"` object beside the hooks
themselves, under `limits`: `timeout` (seconds a hook that names no timeout
of its own may run, default 10), `project_timeout` (the most a hook from
`.janetcode/hooks.json` may ask for, default 60), `output` (bytes kept of
one hook's stdout and stderr, default 65536) and `per_event` (hooks run for
one event, default 20).

```json
"hooks": {
  "post_tool": [{"command": "gofmt -l ."}],
  "limits": {"timeout": 5, "output": 8192}
}
```

A key left out, or zero, means the default; a negative figure is refused on
load (exit 52). A `limits` object in a project's hook file is ignored, with
a note: trusting a checkout's hooks is not a decision to let it choose how
long they may run. The hooks themselves, and what they can do, are
described under Hooks in the interface manual.

### Hooks and the network

A hook from `.janetcode/hooks.json` may not reach the network: trusting a
checkout's hooks is a decision about this checkout, not a decision to hand
the repository everything you can reach. Your own hooks may, and a project
that needs one says so in its README for you to copy across.

A hook says which it is with `"network"`:

```json
"hooks": {
  "post_tool": [
    {"command": "curl -s https://ci.example/notify", "network": true},
    {"command": "grep -rn \"https://\" .", "network": false}
  ]
}
```

`true` is a declaration that the command goes off the machine, and a
project hook that carries it is refused by name. `false` says it does not,
and a project hook that carries it runs even though its command text looks
like it might. Leave the key out and the command text is guessed at
instead, which is what happened to every hook before the key existed: the
guess reads `curl`, `wget`, `ssh`, `rsync` and a URL as reaching out, so it
refuses a `grep` for a URL and cannot see a python one-liner that opens a
socket. Say what the hook does and the guess is not consulted.

### Project instructions

`JANETCODE.md`, `AGENTS.md` or `CLAUDE.md` (first found in a directory
wins) is read from every directory between the repository root and the
working directory, farthest first, so what the repository says about
itself and what a particular package says about itself both reach the
model, with the most specific one last. Without a repository only the
working directory is read: there is no boundary to walk up to, and
walking to the filesystem root would read whatever a home directory
happens to hold.

Nobody writes that file, because writing it from a blank page means
deciding what a stranger needs to know. `/init` in a session, or
`janetcode --init-project` on the command line, writes a starter one from
what the repository already shows: the languages by file count, the
commands it declares (`Makefile` targets, `package.json` scripts, `go.mod`,
`Cargo.toml`, a Python project's files, and `.janetcode/verify.json` if it
is there), the top-level layout, and a `Conventions` heading left empty
because that part is yours. It is a page, not a dump — everything in it is
paid for on every request. An existing `JANETCODE.md` is never overwritten:
`/init force` replaces it, and the command line refuses and says so. When
`AGENTS.md` or `CLAUDE.md` is already there, the generated file names it
and its headings rather than repeating what it says.

A line that is nothing but `@path` pulls another file in where it stands,
which is how one standard is kept in one place and referred to from
several. Paths resolve against the importing file's directory, imports are
followed at most five deep, a file is included once per chain, and a path
outside the project is skipped with a note saying so. An `@` in the middle
of a sentence, or inside a fenced block, is prose.

### Path-scoped rules

Anything in the system prompt is paid for on every request of every
session, which is why project instructions have to stay short — and why
the things worth saying about one corner of a repository usually go
unsaid. A rule says them where they belong. Put a markdown file in
`.janetcode/rules/` (or `rules/` under the config directory, or
`.claude/rules/`) with a `paths:` list in its frontmatter:

```markdown
---
description: migrations are append-only
paths: db/migrations/**, *.sql
---
Never edit a migration that has been applied. Add a new one instead, and
keep the down migration in the same file.
```

Nothing is sent until a tool call touches a matching path; then the rule
is added to the conversation once for the session, as harness text — so
it costs nothing in the sessions and turns it does not apply to. A rule
with no `paths:` is ignored: instructions that always apply belong in
`JANETCODE.md`, where their cost is visible. Globs are matched against
the path the call named, `*` spans anything including `/`, and a glob
without a `/` is matched against the file name too. `/rules` lists what
is defined, what brings each in, and which have been sent this session;
`janetcode --inspect rules` prints the same outside the UI.

#### Cursor rules

A project's Cursor rules in `.cursor/rules/` (`.mdc` or `.md`, nested
folders included, up to 128 files) are read as well; the frontmatter is
Cursor's own. A rule with `alwaysApply: true` joins the project
instructions beside `AGENTS.md`, from the repository root down to the
working directory, and is read at every trust level as `AGENTS.md` is. A
rule with `globs:` (a comma-separated string or a list) is a path-scoped
rule named `cursor:<file>`: it is sent once per session after a tool call
touches a matching path, and it follows the instruction trust level
(`/trust instructions`) like the rest of the path-scoped rules. A rule that
has only a `description` is Cursor's "agent requested" kind and nothing
brings it in here, so it is skipped. Only the `.cursor/rules` at the
project root is searched for glob rules, not those in subdirectories.

### Delegating to Claude Code or Codex: `delegates`

janetcode cannot sign in to a Claude or ChatGPT subscription itself, and
does not try. What it can do is hand a task to the official CLI you are
already signed into. The CLI runs on **its own login and under its own
terms**; janetcode never reads, copies or proxies its credentials, and
the usage is billed to that CLI's account, not to your janetcode provider.
It is off by default:

```json
{"delegates": {"enabled": true}}
```

With it on, the model gets a `delegate` tool whose `agent` is `claude`
(Claude Code) or `codex` (OpenAI Codex CLI), and which of them is used is
the model's choice, so say in your prompt which one you want. The CLI runs
in the session's current directory and its final answer comes back as the
tool result, with its cost and token usage when it reports them. Its
progress shows as the status line while it works.

- **Approval.** A delegate call is put to you like a bash command: the
  CLI is an agent that runs commands. It is withheld in read-only and plan
  modes, and a subagent (`task`) cannot use it.
- **Environment.** The CLI starts with the same scrubbed environment as a
  bash command, so `*_API_KEY` variables (yours for janetcode's providers)
  are not passed on. A CLI that signs in with an API key rather than its
  login therefore has to be run some other way.
- **Read-only by default.** `claude` runs as
  `claude -p PROMPT --output-format stream-json --verbose --permission-mode default --no-session-persistence`
  (headless, where "default" refuses what it would have asked about) and
  `codex` as `codex exec --sandbox read-only --json --ephemeral PROMPT`.
  Nothing bypasses permissions. To let one edit files, replace its `args`
  yourself, for example `"--permission-mode", "acceptEdits"` or
  `"--sandbox", "workspace-write"`; `{prompt}` marks where the task goes
  (it is appended when absent).
- **Limits.** One run is stopped after `timeout_minutes` (default 30); on
  timeout or cancel the CLI and everything it started are killed.

```json
{"delegates": {
  "enabled": true,
  "timeout_minutes": 15,
  "agents": {
    "codex": {"args": ["exec", "--sandbox", "workspace-write", "--json", "--ephemeral", "{prompt}"]},
    "mycli": {"command": "/opt/bin/mycli", "args": ["run", "{prompt}"], "format": "text"}
  }
}}
```

`format` says how stdout is read: `claude` (stream-json), `codex`
(`--json` events) or `text` (all of stdout is the answer, the default for
any other name). An entry replaces only the fields it sets.

## Themes

The `"theme"` key selects the palette: `auto` (detect the terminal
background, the default), `dark`, `light`, or the name of a builtin or a
theme you define. The builtins are `default`, `classic`, `solarized-dark`,
`solarized-light`, `high-contrast`, `muted`, `mono`, `gruvbox`, `nord`,
`catppuccin-mocha`, `catppuccin-latte`, `dracula`, `tokyo-night`,
`one-dark`, `rose-pine`, `everforest`, `kanagawa`, `matrix`, `monokai` and
`github-light`;
`janetcode --inspect themes` lists them, [interface.md](interface.md)
describes them, and `/theme` switches between them live.

A theme names fourteen colour roles — `accent`, `dim`, `faint`, `warn`,
`err`, `ok`, `user`, `tool`, `head_bg`, `head_fg`, `border`,
`border_active`, `diff_add`, `diff_del` — plus a `gutter` (the transcript's
left margin, default 2) and whether it is `dark`. A role's colour is one
string used in both terminal backgrounds, or a `{"light":…, "dark":…}`
pair when it should adapt. A colour is a `#rrggbb` (or `#rgb`) hex or a
`0`–`255` ANSI palette index.

### User themes in the config file

The `"themes"` object maps a name to a theme. Give it a `"base"` and it
inherits every role you do not set, so a tweak is two lines:

```json
{
  "theme": "warm-night",
  "themes": {
    "warm-night": {
      "description": "gruvbox with a brighter prompt caret",
      "dark": true,
      "base": "gruvbox",
      "gutter": 2,
      "roles": {
        "accent": "#fabd2f",
        "border_active": "#fabd2f"
      }
    }
  }
}
```

A theme with no `base` must fill all fourteen roles itself. A bad theme is
a configuration error at startup (exit 52), naming the theme and the field:
an unknown role key, a colour that is neither hex nor an ANSI index, an
unknown base, or a missing role after inheritance. A user theme may share a
builtin's name to override it.

### Theme files

A theme can also live in its own file at `<config-dir>/themes/<name>.json`
(the config directory is the one holding `config.json`, honouring
`XDG_CONFIG_HOME`). The file is the theme object without the outer name —
the file name is the theme name:

```json
{
  "description": "my paper theme",
  "dark": false,
  "base": "solarized-light",
  "roles": { "accent": "#1c72b8" }
}
```

Files are loaded at startup and validated the same way; a name defined in
the config `"themes"` object wins over a file of the same name.

## Prices

`/cost`, the turn summary and the eval reports price tokens from three
sources, in this order: `"prices"` entries in the config file; the price a
provider's model listing carried when the model was picked with `/model`
(OpenRouter lists them); and LiteLLM's public rate table, fetched into
`model-rates.json` beside the config file on the first run with network
and refreshed after a day. The table is the one `ccusage` prices against.
A figure from any of these is an API-equivalent estimate at list price and
is labelled so; only a cost the provider itself reported with the response
(OpenRouter's `usage.cost`, the ai02 broker's `x_aibroker.cost.total_cost`)
is shown as reported, and it wins over every table.

A model no table knows (a broker's own model names show "cost unknown")
is priced with an entry in dollars per million tokens. The key is the model
name or a prefix of it; the longest match wins:

```json
"prices": {"geomind-coding": {"input": 0.3, "output": 1.2, "cached_input": 0.03}}
```

`cached_input` is another name for `cache_read`; `cache_write` is also
accepted. `janetcode --doctor` says which
table is in use and how old it is.
