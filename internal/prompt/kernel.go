package prompt

import "strings"

// A prefix kernel is the system prompt arranged so that every session on
// the same commit sends the same bytes before its first message: the
// cache-stable head, then the project's own instructions and a map of the
// committed tree, and nothing that changes from one session to the next.
// The environment — working directory, date, branch — goes to the end of
// the first user message instead (EnvironmentNote), below the task, so a
// provider's prompt cache matches a second session on the same commit
// from its first call rather than from its second.
//
// The usual prompt puts the environment straight after the head, so two
// sessions in different checkouts of one repository share the head and
// the tool list and nothing below them: on effbench r10–r13 the first
// call of a run read 22% of its prompt from cache, the tool list and the
// head, and the inlined repository below the working directory line was
// billed in full every time.

// SectionKernel is the committed tree's repository map in a kernel
// prompt (see KernelSections).
const SectionKernel = "repository-kernel"

// KernelSections is Sections with the environment taken out and the
// kernel map put in its place, after the project instructions: head,
// project instructions, kernel map, append. Every part of it is the same
// for every session on one commit with one configuration, which
// TestKernelPromptIsByteIdenticalAcrossSessions holds it to. kernelMap ""
// leaves the map out; the environment still moves.
func KernelSections(cwd string, o Override, kernelMap string) []Section {
	var secs []Section
	for _, s := range Sections(cwd, o) {
		switch s.Name {
		case SectionEnvironment:
			continue
		case SectionAppend:
			// The map goes before an appended prompt, as the environment
			// did: --append-system-prompt stays last of all.
			secs = append(secs, kernelSection(kernelMap)...)
			kernelMap = ""
		}
		secs = append(secs, s)
	}
	return append(secs, kernelSection(kernelMap)...)
}

func kernelSection(m string) []Section {
	if strings.TrimSpace(m) == "" {
		return nil
	}
	return []Section{{Name: SectionKernel, Text: "\n# Repository\n" + strings.TrimRight(m, "\n") + "\n"}}
}

// KernelSystem is SystemWith for a kernel prompt.
func KernelSystem(cwd string, o Override, kernelMap string) string {
	var sb strings.Builder
	for _, sec := range KernelSections(cwd, o, kernelMap) {
		sb.WriteString(sec.Text)
	}
	return sb.String()
}

// EnvironmentNote is the environment a kernel prompt leaves out, as a
// block for the end of the session's first user message: the same lines
// the usual prompt carries under its Environment heading.
func EnvironmentNote(cwd string) string {
	return "<environment>\n" + environment(cwd) + "</environment>"
}
