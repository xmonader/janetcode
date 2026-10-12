# TUI review: findings ledger

Every finding the review reported, and what closed it. `verify_fixes.out` shows
that each code fix's tests fail on the code before it; see README.md.

## Findings each reviewer fixed in its own pass

The four area reviews (rendering, input, slash commands and overlays, turns and
dialogs), the follow-up batches and the final sweep fixed these, one commit
each:

- `d59d7117` tui: a line wider than the window pushed the transcript's tail off screen
- `69104e91` tui: the spinner ignored the configured theme until the first /theme
- `bfe451fe` tui: a short modal list panicked or hid the highlighted row
- `cc12a8d3` tui: a tight window opened its transcript on a blank row
- `4ebea3df` tui: /loop armed a loop sending its own arguments
- `4f8f6ceb` tui: /clear and /resume threw away what they had just said
- `d50d833e` tui: /undo never said it had taken the turn out of the context
- `e595f2e5` tui: a permission prompt hid its keys and wiped the message being typed
- `5068e857` tui: /features <consumer> on answered off with no reason while jev was off
- `69fc8d8e` tui: vim undo skipped inserts, j stuck on wrapped lines, cursor ran off line ends
- `14499c74` tui: /mcp add sent a stdio server's env vars to a remote one as headers
- `62889987` tui: /jobs and /agents lists ran the highlight off the screen
- `5f9c2bdf` tui: picker panicked on a terminal a few columns wide
- `76569334` tui: /jobs and /agents opened in silence on an argument naming nothing
- `d0c16b95` tui: prompt keys: CRLF paste, wrapped-line up/down, history past commands
- `5a68ea6a` tui: a cancelled MCP prompt was sent anyway
- `a159de06` tui: a carriage return in tool output drew over the frame
- `af71448e` keymap: alt+ with an upper-case character bound the lower-case key
- `64afc12f` tui: /trajectory's highlight ran off a short terminal
- `17f55848` tui: a prompt saved with CRLF by the external editor came back double-spaced
- `83f0d9a0` tui: /memory show, about and history alone searched for the verb
- `38625159` tui: a !command queued during a turn was sent to the model as text
- `5ced1671` tui: tab on a partial command cut the input back to "/"
- `f4fcb778` tui: /sleep with no provider connected crashed the window
- `4cd14b77` tui: an overlong word was broken onto its own unindented line
- `32754d49` tui: drop a session counter completeSession counted and never read
- `ba124cff` tui: /compact with no provider connected crashed the program
- `ea106fae` tui: a call that finished while scrolled up stayed bright
- `21e20c35` tui: the end of a turn took down a question still taking a late answer
- `c8858c3a` tui: the diff gutter was sized for new line numbers only
- `4cc29365` tui: /connect, /mcp add and the ask box panicked in a window under 8 columns
- `dd923748` tui: a paste into the denial reason doubled its line breaks
- `932c3fad` tui: a tool result kept the fold of the window it was first drawn in
- `67f26386` tui: drop renderOpts.compact, which nothing read
- `a77c0303` tui: mouse copy goes through the model's clipboard writer
- `d9a5fc86` agent, memory, webui: no crash without a provider, and one /loop parser
- `bcae2c7e` tui: help names every command whose argument tab completes
- `d91a5ad5` tui: a one-word change past the right edge was cut off the diff
- `45695cb2` tui: /resume replayed harness notes as typed prompts
- `65938a44` session: generated titles could be model refusals
- `7361ac2e` config: an unpriced listed model read as a known $0
- `2424e834` tui: a new turn forgets the last turn's cost line
- `67bdb78c` agent: a cancelled stream's spend was never counted
- `5034c1f5` prompt: @mentions of paths with spaces never attached
- `55c82f39` tui: slow-command hint stored "bash" and named ctrl+g regardless of keymap
- `c5094e23` session: generated titles came back empty on reasoning models
- `b9c9a24e` agent: go run . and cargo run count as running the edited code
- `8808aa91` tui: an edit to a recalled prompt was lost on the next up or down
- `35a23242` tui: two sessions at the history cap erased each other's prompts
- `fc8d83af` tui: glamour word-wrapped code blocks at punctuation
- `d79edfeb` tui: a full re-render scanned the transcript for every tool call
- `dcec42bf` tui: /help named the default keys after the keybindings moved them
- `6d3e6855` tui: a code block that fits was trimmed and refitted on every render
- `3a90f346` tui: a no-newline marker in a diff was drawn as a numbered line
- `e7e3990e` tui: status lines name the bound keys, not the defaults
- `16165971` tui: a streaming edit was previewed against a file an earlier edit would change
- `8b3489ef` slash, tui, webui: terminal keys leave the shared command descriptions
- `90b0c8fe` tui, i18n: help names keys in the locale's words, and drops an unbound key's clause
- `2a55b312` tui: tab never extends a completion into half a character
- `524f77b7` agent, session: a resumed session keeps its token and cost totals
- `9edb054f` tui, headless, textfmt: no doubled tmux pastes, and headless lays out control characters
- `e7cf3a96` tui: an ambiguous paste can be read either way
- `07c01d2f` tui: /trajectory's lines fit the window, with an ellipsis where cut

## Findings reported as open, or left for another reviewer, and how each closed

| Finding (reported by) | Closed by |
|---|---|
| Crash in narrow dialogs: ask box, /connect, wizard (input) | `4cc29365` |
| Crash in narrow overlays (input) | not reproducible: `4baeba57` draws every overlay at widths 1–12 and heights 1–6 |
| Crash in narrow picker (turn) | `5f9c2bdf` |
| Paste into the deny field: CRLF doubled (input) | `dd923748` |
| Small prompt pastes not cleaned (turn) | `d0c16b95` |
| Slow-command hint stored "bash" and named ctrl+g (input) | `55c82f39` |
| @path with spaces never attached (input) | `5034c1f5` |
| Multibyte filename prefix in completion (input) | `2a55b312` |
| Edits to a recalled history entry lost (input) | `8808aa91` |
| History file overwritten at the cap by a second session (input) | `35a23242` |
| tmux paste without -r double-spaced (input) | `9edb054f`, plus `e7cf3a96`: the same bytes can mean two things, so pasting again flips the reading |
| /compact and /sleep crash, root cause in agent and memory (slash) | `ba124cff`, `f4fcb778`, `d9a5fc86` |
| Web UI /loop parses arguments like the old TUI (slash) | `d9a5fc86` |
| /resume replays harness notes as user prompts (slash) | `45695cb2` |
| Session titles that are model refusals (slash) | `65938a44`, `c5094e23` |
| Help lists only 6 of 11 tab-completing commands (slash) | `bcae2c7e` |
| Unpriced model shown as $0 (turn) | `7361ac2e` |
| Cancelled stream's tokens not counted (turn) | `67bdb78c`; kept over /resume by `524f77b7` |
| Edit preview "past the end" before the edit ran (turn) | not a bug: the preview reads the file as the edit will, pinned by `66782a4b`; the streaming preview gap is fixed by `16165971` |
| Code blocks broken at punctuation at narrow widths (render) | `fc8d83af`, `6d3e6855` |
| No-newline marker drawn as a numbered line (render) | `3a90f346` |
| Quadratic previousResult on re-render (render) | `d79edfeb` |
| \r and bell passed through in headless output (render) | `9edb054f` |
| Previous turn's cost shown if a turn ends without its done event (render) | `2424e834` |
| /help and status lines name default keys after a rebind (input follow-up) | `dcec42bf`, `e7e3990e` |
| Shared slash descriptions name terminal keys (input follow-up) | `8b3489ef` |
| French /help shows raw key names; "unbound" in translated help (help fix) | `90b0c8fe` |
| Verdict "nothing was run" after `go run .` (live smoke test) | `b9c9a24e` |
| /trajectory lines cut mid-word at 40 columns (final sweep) | `07c01d2f` |
| Flaky webui recipe-queue test (full-suite run) | `8b3489ef` |
| Flaky headless stream-input test (race run) | `1d60da0d` |

Nothing reported in the review is left open.
