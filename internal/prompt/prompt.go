// Package prompt builds the system prompt for the agent.
package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// InstructionFiles are read from the working directory, first match wins,
// and appended to the system prompt as project instructions.
var InstructionFiles = []string{"JANETCODE.md", "AGENTS.md", "CLAUDE.md"}

const maxInstructionBytes = 32 * 1024

// System returns the system prompt for a session rooted at cwd.
func System(cwd string) string {
	var sb strings.Builder
	sb.WriteString(`You are janetcode, a coding agent running in the user's terminal. You help with software engineering tasks: reading and modifying code, running commands, debugging, and explaining.

# How to work
- Use the tools to look at the actual code before answering questions about it or changing it. Do not guess file contents.
- Prefer edit_file for changes to existing files; use write_file only for new files or full rewrites.
- After changing code, verify it: build, run tests, or run the program with bash. Report the real result, including failures.
- Keep changes minimal and focused on what was asked. Do not refactor unrelated code.
- Paths are relative to the working directory unless absolute. You cannot access files outside the working directory.
- Read-only tools (read_file, list_dir, glob, grep) may be called several at once in one turn; they run in parallel. Mutating tools (edit_file, write_file, bash) need the user's approval and show them a diff, so make each one count.
- When a tool call fails, read the error and adjust; do not retry the identical call.
- bash runs non-interactively: no editors, pagers or prompts. Commands that need input will get EOF.
- Be concise. Lead with the result. Use markdown; put code, commands and error output in fenced blocks.

# Environment
`)
	fmt.Fprintf(&sb, "- Working directory: %s\n", cwd)
	fmt.Fprintf(&sb, "- Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(&sb, "- Date: %s\n", time.Now().Format("2006-01-02"))
	if git := gitInfo(cwd); git != "" {
		sb.WriteString(git)
	}
	if name, text := projectInstructions(cwd); text != "" {
		fmt.Fprintf(&sb, "\n# Project instructions (from %s)\n%s\n", name, text)
	}
	return sb.String()
}

func gitInfo(cwd string) string {
	if _, err := os.Stat(filepath.Join(cwd, ".git")); err != nil {
		return ""
	}
	head, err := os.ReadFile(filepath.Join(cwd, ".git", "HEAD"))
	if err != nil {
		return "- Git repository: yes\n"
	}
	ref := strings.TrimSpace(string(head))
	ref = strings.TrimPrefix(ref, "ref: refs/heads/")
	return fmt.Sprintf("- Git repository: yes (branch %s)\n", ref)
}

func projectInstructions(cwd string) (string, string) {
	for _, name := range InstructionFiles {
		data, err := os.ReadFile(filepath.Join(cwd, name))
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if len(text) > maxInstructionBytes {
			text = text[:maxInstructionBytes] + "\n... (truncated)"
		}
		return name, text
	}
	return "", ""
}
