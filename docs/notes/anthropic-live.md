# The Anthropic provider, verified live (2026-09-29)

Until this date the `anthropic` provider (official SDK, Messages API) had
only been exercised against fake servers in the unit tests. It was run
end to end against a real Messages API endpoint: the Hero AI broker
(`https://api.ai02.ourhero.ai`), which serves `/v1/messages` for every
model it routes, including Anthropic's own. The broker authenticates with
`Authorization: Bearer`, which is what made the run possible only after
the first fix below.

Setup, as in [configuration](../manual/configuration.md):

```sh
export ANTHROPIC_AUTH_TOKEN=sk-hab-v1-...
export ANTHROPIC_BASE_URL=https://api.ai02.ourhero.ai
janetcode --provider anthropic --model <model> ...
```

Wire checks were made through a logging proxy between janetcode and the
broker (every request body and header recorded, responses streamed
through).

## What was run

| run | model | what it exercised | outcome |
|---|---|---|---|
| `-p` fix a failing Go test, `--output-format json` | deepseek/deepseek-v4.1-flash | streaming, tool use over 4 model calls (bash, read, edit), verdict | exit 0, "✓ verified: 1 file edited and tests ran and passed afterwards" |
| same task | xiaomi/mimo-v2.6-flash | same | exit 0, verified, 5 tool calls |
| same task | qwen/qwen3.8-27b | same | exit 0, "tests that had been failing now pass" |
| list and read, `--effort low`, stream-json | anthropic/claude-haiku-4.5 | parallel tool calls in one message, `output_config.effort` sent, no `thinking` (Haiku) | exit 0; every `tool_result` paired with a `tool_start` |
| add a function and a test | anthropic/claude-sonnet-4.6 | adaptive thinking (`thinking: {type: adaptive, display: summarized}`) on every call, 5 calls | exit 0, verified: 2 files edited, tests pass |
| arithmetic then check, `--effort high` | anthropic/claude-sonnet-4.6 | effort + adaptive thinking together | exit 0 (and see the bug it found, below) |
| 1500-word essay, SIGINT after ~6 s | deepseek/deepseek-v4.1-flash | cancellation mid-stream | exit 130, "context canceled" |
| `-c -p "tell me my codeword"` | deepseek/deepseek-v4.1-flash | resuming the cancelled session: history still valid for the API | answered "PELICAN" from the cancelled turn's prompt |
| `--doctor` | deepseek/deepseek-v4.1-flash | the doctor's live model call on this path | "model call … answered in 895ms" |
| through a TLS proxy with a private CA, `JANETCODE_CA_FILE` | deepseek/deepseek-v4.1-flash | the provider's HTTP client picks up extra roots | refused without the CA, answered with it |

On the wire: only `Authorization: Bearer …` was sent (no `x-api-key`
beside it), `anthropic-version: 2023-06-01`, top-level `cache_control`
plus breakpoints on both system blocks, and 23 tool definitions.

## What it found (all fixed, each with a test)

- **No bearer auth.** The provider sent only `x-api-key`, which the
  broker (like most gateways in front of the Messages API) answers with
  401. Added `"auth": "bearer"` on a provider entry and
  `ANTHROPIC_AUTH_TOKEN`, read as Claude Code reads it.
- **Two credential headers.** The SDK read `ANTHROPIC_API_KEY`,
  `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_BASE_URL` itself, beneath the
  config's own reading of them, so a key in the environment added a second
  auth header beside the one the config chose. The SDK's environment
  defaults are now off; the config is the one reader.
- **A tool call written as text.** Sonnet 4.6 through the broker ended a
  turn with `[tool_use]{"id":…,"name":"bash","input":{…}}` in its answer
  instead of a tool_use block; the test it meant to run never ran and the
  turn was graded unverified. The prose-call recovery now reads that shape.
- **Memory from musings.** In the same run the memory extractor's reply
  carried on the coding conversation ("Wait, that output would be a
  pass… Let me re-read.", "[tool_result]--- FAIL: …"), and the parser's
  plain-line fallback stored six of those lines as project facts. A reply
  that reads as the agent talking is no longer stored.
- **Run-together answers.** The `-p` result (json, stream-json's result
  line and text mode's stdout) joined the text of successive model calls
  with nothing between them: "I'll list the files.The files are …". A
  tool between two calls' text now leaves a paragraph break. This was on
  every provider, not only this one.

## What could not be checked here

- **Cache hits.** The broker reports no `cache_read_input_tokens` or
  `cache_creation_input_tokens`, even for a direct curl request carrying
  `cache_control` twice in a row with the same 6.6k-token prefix. So
  janetcode's markers were verified on the wire, but not the discount; the
  mapping of those usage fields is covered by the unit tests
  (`cache_test.go`).
- **Visible thinking text.** Sonnet 4.6 chose not to think on these small
  tasks, so no thinking block (and no signature round trip) came back. The
  round trip is covered by `TestStreamMapsRequestAndResponse`.
- **The Anthropic API itself** (`api.anthropic.com` with an `sk-ant-` key):
  no key was available. The broker routes to Anthropic's models, so the
  model behaviour is real; the endpoint is the broker's.
