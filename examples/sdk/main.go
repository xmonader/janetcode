// Command sdk-example runs one janetcode prompt from Go and prints what
// happened: the streamed answer, each tool call, and the result's
// verdict and usage. Edits are applied without asking; any other call
// janetcode would ask about is put to you on the terminal.
//
//	go run ./examples/sdk -dir ~/src/project "add a test for Parse"
//
// It needs the janetcode binary on PATH (or -bin) and a provider key in
// the environment, exactly as janetcode itself does.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/xmonader/janetcode/sdk"
)

func main() {
	bin := flag.String("bin", "", "janetcode binary (default: janetcode on PATH)")
	dir := flag.String("dir", ".", "working directory")
	model := flag.String("model", "", "model override")
	provider := flag.String("provider", "", "provider override (openai, anthropic, or one from the config)")
	var servers []sdk.MCPServer
	flag.Func("mcp", `an MCP server for the session, "name=command arg..." or "name=https://url" (repeatable)`, func(v string) error {
		name, spec, ok := strings.Cut(v, "=")
		if !ok || name == "" || spec == "" {
			return fmt.Errorf("want name=command or name=url")
		}
		if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") {
			servers = append(servers, sdk.MCPServer{Name: name, URL: spec})
			return nil
		}
		f := strings.Fields(spec)
		servers = append(servers, sdk.MCPServer{Name: name, Command: f[0], Args: f[1:]})
		return nil
	})
	flag.Parse()
	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: sdk-example [-dir path] [-model m] <prompt>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	in := bufio.NewReader(os.Stdin)
	res, err := sdk.Run(ctx, sdk.Options{
		Binary: *bin, Dir: *dir, Model: *model, Provider: *provider, Mode: sdk.ModeAutoEdits, MCPServers: servers,
		OnEvent: func(e sdk.Event) {
			switch e.Kind {
			case sdk.EventText:
				fmt.Print(e.Text)
			case sdk.EventToolCall:
				fmt.Printf("\n→ %s\n", e.Tool.Title)
			}
		},
		Approve: func(_ context.Context, p sdk.Permission) sdk.Decision {
			fmt.Printf("\nallow %s? [y/N/a] ", p.Title)
			line, _ := in.ReadString('\n')
			switch strings.TrimSpace(strings.ToLower(line)) {
			case "y", "yes":
				return sdk.AllowOnce
			case "a", "always":
				return sdk.AllowAlways
			}
			return sdk.Deny
		},
	}, prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("\n\nsession %s · %s · %d tool calls · in %d / out %d tokens",
		res.SessionID, res.StopReason, len(res.ToolCalls), res.Usage.InputTokens, res.Usage.OutputTokens)
	if res.Verdict != "" {
		fmt.Printf(" · %s", res.Verdict)
	}
	fmt.Println()
}
