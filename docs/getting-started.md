# Getting started

Ten minutes from nothing to a change you have reviewed and kept. The
[manual](manual/README.md) is the reference for everything after that.

## 1. Install (1 minute)

On Linux or macOS:

```sh
curl -fsSL https://raw.githubusercontent.com/xmonader/janetcode/master/install.sh | sh
```

It checks the download against the release's `SHA256SUMS` and puts
`janetcode` in `~/.local/bin`. With Go 1.25 or later you can build it
instead: `go install github.com/xmonader/janetcode/cmd/janetcode@latest`.

## 2. Connect a model (2 minutes)

Open a terminal in a project you know. It should be a git repository;
that is what makes undo reliable.

```sh
cd ~/code/your-project
janetcode
```

The first run opens the connect dialog. Pick a provider (Anthropic,
OpenAI, OpenRouter, a local Ollama and a dozen more), paste the key and
choose a model. The choice is saved, so this happens once. If you already
export `ANTHROPIC_API_KEY`, janetcode uses it and skips the dialog; an
`OPENAI_API_KEY` needs the model named too (`janetcode --model gpt-5`,
or `OPENAI_BASE_URL` and a model for any OpenAI-compatible server).

`janetcode --doctor` checks the config, the key and a live model call if
anything seems wrong.

## 3. Ask for something small (3 minutes)

Type a request the way you would to a colleague, and press enter:

```
the README's install section still says Go 1.22; make it match go.mod
```

The model reads files freely. Anything that changes something stops at a
prompt first and shows you exactly what it is about to do:

| key | at a permission prompt |
|---|---|
| `y` | allow this one call |
| `a` | allow this tool for the rest of the session |
| `r` | save a rule, such as `bash(go test *)`, so it never asks again |
| `n` | deny; you can type why, and the model tries another way |
| `c` | cancel the whole turn |

`esc` stops a turn that is going somewhere you did not mean. `@file`
attaches a file to your message, and `!command` runs a command and shows
its output to both of you.

## 4. Review and keep, or undo (2 minutes)

```
/diff      what changed against HEAD, file by file
/undo      put back everything the last turn changed
```

`/undo` works on the whole turn, including files a command created. When
you are happy, commit the change the way you always do; janetcode only
commits when you ask it to (and, outside yolo, asks before `git commit`).

## 5. Decide how much it may do on its own (1 minute)

`/permissions` shows, in one place, what runs without asking, what asks
first and what never runs. `shift+tab` moves between the modes:

- **ask** (the default): edits and commands ask first.
- **plan**: the model explores and writes a plan, and changes nothing.
- **auto**: edits in the workspace and read-only commands run; the rest asks.
- **yolo**: everything runs, except writes to protected paths such as
  shell startup files, git hooks and keys. Use it in a container or a
  throwaway checkout.

## 6. Tell it about the project (1 minute)

```
/init
```

writes `JANETCODE.md` from what the repository shows: languages, the
build and test commands, the layout. Edit it like any other file; it is
read at the start of every session, so house rules belong there.

## Where to go next

- `janetcode -p "prompt"` runs one request without the interface and
  prints the answer, for scripts and CI.
- `janetcode --web` opens the same session in a browser.
- `janetcode --bg "prompt"` runs a request in the background. When it
  needs permission it waits; `janetcode --sessions` shows it and
  `janetcode --answer <id> allow` lets it continue.
- `janetcode -c` continues the last session in this directory.
- `/help` lists the everyday commands; `/help <question>`, such as
  `/help undo one file`, searches every feature and the manual.
