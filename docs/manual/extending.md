# Extending and embedding

Part of the [janetcode manual](README.md). MCP servers, hooks, skills,
custom commands and agent profiles are covered in
[the interface](interface.md); this page is about driving janetcode
from other programs.

## The Go SDK

`github.com/xmonader/janetcode/sdk` runs janetcode from a Go program.
It drives the janetcode binary over the Agent Client Protocol, the
stdio protocol an editor uses (`janetcode --acp`), so the program gets
the whole agent as the terminal has it: the same tools, permission
model, hooks, verification gates, memory and session files. There is no
second, thinner copy of the loop to fall behind.

```go
res, err := sdk.Run(ctx, sdk.Options{Dir: repo, Mode: sdk.ModeAutoEdits}, "make the tests pass")
if err != nil {
	return err
}
fmt.Println(res.Text)       // the answer
fmt.Println(res.Verdict)    // what the turn demonstrated, e.g. "✓ tested: go test ./... passed"
fmt.Println(res.Usage.OutputTokens, len(res.ToolCalls))
```

For more than one prompt, start a client and open a session; the
conversation carries over between prompts, and the session is saved
like any other, so `janetcode --resume <id>` opens it in the terminal:

```go
c, err := sdk.Start(ctx, sdk.Options{
	Dir:  repo,
	Mode: sdk.ModeAsk,
	Approve: func(ctx context.Context, p sdk.Permission) sdk.Decision {
		if p.Kind == "edit" {
			return sdk.AllowOnce
		}
		return sdk.Deny
	},
	OnEvent: func(e sdk.Event) {
		if e.Kind == sdk.EventText {
			fmt.Print(e.Text)
		}
	},
})
defer c.Close()
s, _ := c.NewSession(ctx) // or c.LoadSession(ctx, id)
res, _ := s.Prompt(ctx, "why does TestParse fail?")
res, _ = s.Prompt(ctx, "fix it")
```

The options:

- `Binary`: the janetcode executable. The default is `janetcode` on
  `PATH`.
- `Dir`: the working directory. The default is the current directory.
- `Model` and `Provider`: override the configured model and provider,
  as `--model` and `--provider` do.
- `Mode`: what runs without asking. `ModeAsk` is the default;
  `ModeAutoEdits`, `ModeAuto`, `ModeReadOnly` and `ModeYolo` are the
  `--auto-edits`, `--auto`, `--readonly` and `--yolo` modes.
- `Approve`: answers every call the mode leaves to ask about with
  `AllowOnce`, `AllowAlways` or `Deny`. The `Permission` it gets
  carries the tool, a title, the kind (`read`, `edit`, `execute`, …),
  the input, the diff or command as a preview, and the paths touched.
  A nil `Approve` denies everything, so an unattended program never
  waits on a question nobody will answer. The model is told the call
  was refused and carries on.
- `OnEvent`: receives the streamed answer, reasoning and tool calls in
  order. It runs on the goroutine that reads the connection, so it
  must return promptly.
- `Env`, `Args` and `Stderr`: extra environment entries (a provider
  key, say), extra flags (`--set k=v`, `--add-dir`), and where
  janetcode's diagnostics go. By default they are discarded.

A `Result` holds the answer text, the stop reason (`end_turn`,
`cancelled`, `max_turn_requests`, `refusal`), every tool call with its
status and output, the token usage and reported cost, and the verdict.
The verdict is what the harness saw the turn demonstrate, not what the
model claimed; it is the same line `-p` prints. Cancelling the context
passed to `Prompt` cancels the turn over the protocol, so the agent
stops cleanly and the result says `cancelled`.

The usage and verdict ride in the ACP prompt result under
`_meta.janetcode` (`verdict`, `usage`, `toolCalls`). An editor that does
not know the key ignores it; any other ACP client can read it too.

`examples/sdk` is a small program built on it. It streams the answer,
applies edits, and asks on the terminal about anything else:

```sh
go run ./examples/sdk -dir ~/src/project "add a test for Parse"
```
