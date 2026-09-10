# janetcode against Claude Code, same model, same endpoint

- Model: `qwen/qwen3.7-plus` at `https://openrouter.ai/api/v1` for both
  harnesses. Claude Code reaches it through `cmd/modelproxy`, which
  accepts the model name Claude Code insists on and forwards to
  OpenRouter's Anthropic-compatible endpoint with the name swapped; the
  request body otherwise passes through unchanged
- Claude Code 2.1.267, `-p --output-format json --dangerously-skip-permissions`,
  under an empty `CLAUDE_CONFIG_DIR` so none of the operator's skills,
  memory or instructions enter its prompt; janetcode with its defaults
- Three task sets, three runs each: the twenty single-prompt tasks
  (`01–20`, 3-minute budget), the hard six (`21–26`, 10 minutes, call cap
  60) and the three multi-prompt sessions (`41–43`, 5 minutes per prompt)
- 174 task runs per harness, 2026-09-10

## Totals

| set | harness | passed | fresh input | cache read | output | total read | model calls | wall | cost at list |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| easy, 60 | **janetcode** | **60/60** | 287,103 | **682,888** | **21,909** | **991,900** | **217** | **523s** | **$0.16** |
| easy, 60 | Claude Code | 52/60 | 1,902 | 6,068,485 | 37,782 | 6,108,169 | 348 | 913s | $0.44 |
| hard, 18 | **janetcode** | **18/18** | 123,517 | **327,924** | **44,576** | **496,017** | **83** | **688s** | **$0.12** |
| hard, 18 | Claude Code | 16/18 | 552 | 1,897,930 | 62,499 | 1,960,981 | 146 | 959s | $0.20 |
| session, 9 | **janetcode** | **9/9** | 317,012 | **2,436,662** | **44,433** | **2,798,107** | **213** | **777s** | **$0.31** |
| session, 9 | Claude Code | **9/9** | 1,332 | 6,699,370 | 54,256 | 6,754,958 | 299 | 817s | $0.50 |

Cost at OpenRouter's list price for this model: $0.32 per million fresh,
$0.064 cache read, $1.28 output. Claude Code's own `total_cost_usd` is
computed from Anthropic's sonnet prices and is meaningless here; it is
ignored.

## What the numbers say

**The prompt is the difference.** Claude Code sends the model a
27,700-character system prompt and 21 tool schemas on every call;
janetcode's whole prefix is about 1.8k tokens. On this endpoint nearly
all of Claude Code's prompt is credited as cached — its fresh column is a
few hundred tokens — so the gap shows up in cache reads: 6.1M against
1.0M on the easy set, 1.9M against 0.5M on the hard set, 6.7M against
2.8M on the sessions. At this model's cache discount that is 2.7x, 1.7x
and 1.6x the bill. On an endpoint with a smaller discount, or none, the
gap would be the token gap: six, four and two and a half times.

**Claude Code takes more model calls to do the same work**: 348 against
217, 146 against 83, 299 against 213. It also writes more output on
every set. With the same model on the other end, those are the harness's
choices, not the model's.

**On pass rate janetcode is ahead on the single-prompt sets and level on
sessions.** 60/60 against 52/60; 18/18 against 16/18; 9/9 each.

## Why Claude Code lost tasks with this model

Eight of its ten failures are the same thing: a file written somewhere
other than the task directory. `08-shell-script` all three runs,
`16-readme`, `19-write-test`, `04-python-fizzbuzz` — the check found no
`count.sh`, no `README.md`, no test file, no `fizzbuzz.py`. The files
were written: during the smoke tests one landed in the operator's home
directory and one in `/tmp`, and the runs left `count.sh` in `/tmp`.

Claude Code's `Write` tool requires an absolute path, and its system
prompt states the working directory once, in 27,700 characters. Asked
through the same route, with no tools, to quote the working directory
its instructions gave, qwen3.7-plus replied that none was given. The
model has the path and loses it. Tasks that only read files passed: the
`Read`, `Glob` and `Bash` tools default to the directory. Claude Code's
own models presumably do not lose it, and this comparison cannot say
anything about them; it says what happens when this harness drives a
model that is not Claude.

The other two: `17-csv-sort` twice on the easy set, and on the hard set
`23-python-ini-parser` (hidden tests) and `26-log-summary-script` once
each.

## Reproducibility

Fresh input is not the figure to compare for Claude Code here — it is
nearly all cache — so total read per run:

| set | janetcode | Claude Code |
|---|---|---|
| easy | 334,332 · 329,095 · 328,473 (2%) | 2,040,499 · 2,116,237 · 1,951,433 (8%) |
| hard | 163,109 · 154,077 · 178,831 (16%) | 782,614 · 529,685 · 648,682 (48%) |
| session | 866,305 · 1,045,908 · 885,894 (21%) | 2,211,155 · 2,233,785 · 2,310,018 (4%) |

The easy set reproduces to within 8% for both. The hard set swings 48%
for Claude Code, driven by one run of the INI parser; the sessions
swing 21% for janetcode, which is the spread this set has shown before.
None of the between-harness gaps above is inside its spread.

## Caveats

- One model, one endpoint. The earlier `samemodel` series did this on
  mercury-2.5 for the sessions only and found the same ordering.
- Claude Code was designed around Claude models; running it on qwen
  measures its harness (prompt, tools, loop) with the model held equal,
  which is the question, but its failures on file placement may not
  occur with its own models.
- Claude Code's `--output-format json` without verbose mode carries no
  transcript, so its tool calls are not on record; the file-placement
  cause is from the smoke tests, where they were.
- The proxy forwards Claude Code's requests unchanged. Whether Anthropic's
  own endpoint would cache Claude Code's prompt as generously as
  OpenRouter's Alibaba route did is not measured here.

## Reproduce

```
go build -o bin/modelproxy ./cmd/modelproxy
./bin/modelproxy -addr 127.0.0.1:8820 -model qwen/qwen3.7-plus -key $OPENROUTER_API_KEY &
export CLAUDE_CONFIG_DIR=$(mktemp -d) ANTHROPIC_BASE_URL=http://127.0.0.1:8820 \
  ANTHROPIC_AUTH_TOKEN=proxy CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
./bin/harnessbench --harnesses janetcode,claudecode --claude-model sonnet \
  --model qwen/qwen3.7-plus --base-url https://openrouter.ai/api/v1 --out easy/run1
# hard: add --filter all --only hard --timeout 10m --max-turns 60
# session: add --evals evals/session --filter session --timeout 5m
```
