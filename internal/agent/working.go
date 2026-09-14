package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// WorkingMemory is the typed record that replaces the prose handoff note
// when the history is compacted. The harness fills the parts it observed
// itself (files edited and read, commands run and how they exited, the
// last verification) exactly, from the messages being summarised; the
// model only updates the parts that need judgement (goal, decisions,
// what was ruled out, facts, open questions, next steps), starting from
// the previous record so nothing drifts from summary to summary.
type WorkingMemory struct {
	Goal   string
	Latest string // the current user request
	// Asks are the user's requests so far, verbatim prefixes, so the
	// literal requirements survive a chain of summaries instead of
	// eroding through paraphrase. Goal is the first of them.
	Asks     []string
	Edited   []FileNote
	Read     []string
	Commands []CommandNote
	Verify   string // last verification outcome
	// Todos is the part of the plan still open, carried across a
	// compaction so the next context knows what is left rather than
	// rediscovering it.
	Todos []Todo
	// Model-maintained sections.
	Decisions []string `json:"decisions"`
	RuledOut  []string `json:"ruled_out"`
	Facts     []string `json:"facts"`
	Open      []string `json:"open"`
	Next      []string `json:"next"`
	// Notes holds the model's prose when it did not return the structure.
	Notes string

	seen map[string]bool // tool_use ids already counted
}

// FileNote is a file the agent changed, with a hash of its content at
// compaction time so a later read can tell whether it moved since.
type FileNote struct {
	Path  string
	Edits int
	Hash  string
}

// CommandNote is a shell command the agent ran.
type CommandNote struct {
	Command string
	Runs    int
	Exit    string
	order   int
}

const (
	workingMarker = "[Context was compacted. Working memory for this session:]"
	maxEdited     = 40
	maxRead       = 40
	maxCommands   = 15
	maxItems      = 12
	maxAsks       = 8
	askChars      = 300
	// richSlots is how many harness-observed facts a head must hold for
	// the model-maintained sections to be carried forward without a new
	// summary call (see rich).
	richSlots = 12
)

const workingSystem = `You are compacting a coding-agent conversation so work can continue in a fresh context. The harness has already recorded exactly which files were edited and read, which commands ran and how they exited, and the last verification result; do not repeat those. Update the working memory from the conversation and return only a JSON object with these keys:
"goal": the user's goal and every constraint or preference they stated (string);
"decisions": choices made and why (array of strings);
"ruled_out": approaches tried and abandoned, each with the reason, so they are not repeated (array);
"facts": things learned about the codebase that would be expensive to rediscover, with concrete file paths, names and commands (array);
"open": unresolved questions (array);
"next": what remains, in order, concretely (array).
Keep entries from the previous record that still matter, drop what is finished, and be precise rather than brief. Output only the JSON object.`

var reExitArrow = regexp.MustCompile(`→ exit (\d+)`)

// workingSchema is the shape the model must answer the compaction call
// in. The record is parsed, not read, so a reply that is nearly JSON
// costs the whole summary — merge falls back to keeping it as prose and
// the structure is lost. A provider that can constrain the reply is asked
// to; one that cannot ignores this and the parse behaves as before.
var workingSchema = &llm.Schema{
	Name:   "working_memory",
	Strict: true,
	JSON: json.RawMessage(`{
"type":"object",
"properties":{
"goal":{"type":"string"},
"decisions":{"type":"array","items":{"type":"string"}},
"ruled_out":{"type":"array","items":{"type":"string"}},
"facts":{"type":"array","items":{"type":"string"}},
"open":{"type":"array","items":{"type":"string"}},
"next":{"type":"array","items":{"type":"string"}}},
"required":["goal","decisions","ruled_out","facts","open","next"],
"additionalProperties":false}`),
}

// absorb records the tool calls and user requests in msgs that were not
// counted yet.
func (w *WorkingMemory) absorb(msgs []llm.Message) {
	if w.seen == nil {
		w.seen = map[string]bool{}
	}
	uses := map[string]llm.Block{}
	edited := map[string]int{}
	for i, f := range w.Edited {
		edited[f.Path] = i
	}
	cmds := map[string]int{}
	for i, c := range w.Commands {
		cmds[c.Command] = i
	}
	order := len(w.Commands)
	for _, m := range msgs {
		for _, b := range m.Blocks {
			switch b.Type {
			case llm.BlockText:
				if m.Role == llm.RoleUser && !strings.HasPrefix(b.Text, "[") && strings.TrimSpace(b.Text) != "" {
					if w.Goal == "" {
						w.Goal = prefix(b.Text, 600)
					}
					w.Asks = appendUnique(w.Asks, prefix(b.Text, askChars))
				}
			case llm.BlockToolUse:
				uses[b.ID] = b
			case llm.BlockToolResult:
				u, ok := uses[b.ToolUseID]
				if !ok || b.IsError || w.seen[b.ToolUseID] {
					continue
				}
				w.seen[b.ToolUseID] = true
				var in struct {
					Path    string `json:"path"`
					Command string `json:"command"`
				}
				json.Unmarshal(u.Input, &in)
				switch u.Name {
				case "write_file", "edit_file":
					if in.Path == "" {
						continue
					}
					if i, ok := edited[in.Path]; ok {
						w.Edited[i].Edits++
					} else {
						edited[in.Path] = len(w.Edited)
						w.Edited = append(w.Edited, FileNote{Path: in.Path, Edits: 1})
					}
				case "read_file":
					if in.Path != "" {
						w.Read = appendUnique(w.Read, in.Path)
					}
				case "bash":
					if in.Command == "" {
						continue
					}
					exit := "0"
					if m := reExitCode.FindStringSubmatch(b.Text); m != nil {
						exit = m[1]
					} else if m := reExitArrow.FindStringSubmatch(b.Text); m != nil {
						exit = m[1]
					}
					order++
					if i, ok := cmds[in.Command]; ok {
						w.Commands[i].Runs++
						w.Commands[i].Exit = exit
						w.Commands[i].order = order
					} else {
						cmds[in.Command] = len(w.Commands)
						w.Commands = append(w.Commands, CommandNote{Command: in.Command, Runs: 1, Exit: exit, order: order})
					}
				}
			}
		}
	}
	if len(w.Read) > maxRead {
		w.Read = w.Read[len(w.Read)-maxRead:]
	}
	if len(w.Asks) > maxAsks {
		w.Asks = append(w.Asks[:1:1], w.Asks[len(w.Asks)-maxAsks+1:]...) // keep the first and the latest
	}
	if len(w.Commands) > maxCommands {
		sort.SliceStable(w.Commands, func(i, j int) bool { return w.Commands[i].order > w.Commands[j].order })
		w.Commands = w.Commands[:maxCommands]
		sort.SliceStable(w.Commands, func(i, j int) bool { return w.Commands[i].order < w.Commands[j].order })
	}
	if len(w.Edited) > maxEdited {
		w.Edited = w.Edited[len(w.Edited)-maxEdited:]
	}
}

func appendUnique(list []string, s string) []string {
	for i, x := range list {
		if x == s {
			return append(list[:i:i], append(list[i+1:], s)...)
		}
	}
	return append(list, s)
}

// rich reports whether the model-maintained sections can be carried
// forward unchanged: an earlier summary wrote them, the head being
// compacted brought no new user request the model must weigh, and the
// harness observed enough of it — files changed, commands run — that the
// record already tells the next context what happened. The summary call
// is then skipped; it is the one part of compaction that costs tokens.
func (w *WorkingMemory) rich(head []llm.Message) bool {
	if len(w.Decisions)+len(w.Facts)+len(w.Next) == 0 {
		return false
	}
	slots := 0
	for _, m := range head {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && m.Role == llm.RoleUser && !strings.HasPrefix(b.Text, "[") && strings.TrimSpace(b.Text) != "" {
				return false
			}
			if b.Type != llm.BlockToolUse {
				continue
			}
			switch b.Name {
			case "write_file", "edit_file", "bash":
				slots += 2
			case "read_file":
				slots++
			}
		}
	}
	return slots >= richSlots
}

// hash records the current content hash of every edited file under root.
func (w *WorkingMemory) hash(root string) {
	for i, f := range w.Edited {
		p := f.Path
		if !filepath.IsAbs(p) && root != "" {
			p = filepath.Join(root, p)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			w.Edited[i].Hash = "missing"
			continue
		}
		sum := sha256.Sum256(data)
		w.Edited[i].Hash = hex.EncodeToString(sum[:4])
	}
}

// modelSections is the part of the record the model maintains.
func (w *WorkingMemory) modelSections() string {
	data, _ := json.MarshalIndent(struct {
		Goal      string   `json:"goal"`
		Decisions []string `json:"decisions"`
		RuledOut  []string `json:"ruled_out"`
		Facts     []string `json:"facts"`
		Open      []string `json:"open"`
		Next      []string `json:"next"`
	}{w.Goal, nonNil(w.Decisions), nonNil(w.RuledOut), nonNil(w.Facts), nonNil(w.Open), nonNil(w.Next)}, "", "  ")
	return string(data)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// merge applies the model's answer. It returns false, keeping the text
// as prose notes, when the answer is not the expected JSON object.
func (w *WorkingMemory) merge(answer string) bool {
	answer = strings.TrimSpace(answer)
	var got struct {
		Goal      string   `json:"goal"`
		Decisions []string `json:"decisions"`
		RuledOut  []string `json:"ruled_out"`
		Facts     []string `json:"facts"`
		Open      []string `json:"open"`
		Next      []string `json:"next"`
	}
	if llm.ExtractJSONObject(answer, &got) != nil {
		w.Notes = answer
		return false
	}
	w.Notes = ""
	if got.Goal != "" {
		w.Goal = got.Goal
	}
	w.Decisions, w.RuledOut, w.Facts, w.Open, w.Next = capItems(got.Decisions), capItems(got.RuledOut), capItems(got.Facts), capItems(got.Open), capItems(got.Next)
	return true
}

func capItems(items []string) []string {
	var out []string
	for _, s := range items {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	if len(out) > maxItems {
		out = out[:maxItems]
	}
	return out
}

// WorkingMemory renders the record the agent carries across compactions,
// so it can be looked at without waiting for the next compaction to show
// it. It is empty until something has been compacted.
func (a *Agent) WorkingMemory() string {
	if len(a.working.Edited)+len(a.working.Read)+len(a.working.Commands)+len(a.working.Decisions)+len(a.working.Facts)+len(a.working.Todos) == 0 && a.working.Goal == "" {
		return ""
	}
	return a.working.Render()
}

// Render writes the record as the note the model reads after compaction.
func (w *WorkingMemory) Render() string {
	var b strings.Builder
	b.WriteString(workingMarker + "\n")
	if w.Goal != "" {
		fmt.Fprintf(&b, "\nGoal: %s\n", w.Goal)
	}
	if w.Latest != "" && w.Latest != w.Goal {
		fmt.Fprintf(&b, "\nCurrent request: %s\n", prefix(w.Latest, 600))
	}
	if len(w.Asks) > 1 {
		b.WriteString("\nUser requests so far (verbatim):\n")
		for i, ask := range w.Asks {
			fmt.Fprintf(&b, "%d. %s\n", i+1, ask)
		}
	}
	if len(w.Edited) > 0 {
		b.WriteString("\nFiles edited (content hash now; re-read before editing if the hash changed):\n")
		for _, f := range w.Edited {
			fmt.Fprintf(&b, "- %s  %s  %s\n", f.Path, textfmt.Plural(f.Edits, "edit"), f.Hash)
		}
	}
	if len(w.Read) > 0 {
		b.WriteString("\nFiles read: " + strings.Join(w.Read, ", ") + "\n")
	}
	if len(w.Commands) > 0 {
		b.WriteString("\nCommands run (last exit):\n")
		for _, c := range w.Commands {
			fmt.Fprintf(&b, "- %s → exit %s", quoteTrunc(c.Command, 100), c.Exit)
			if c.Runs > 1 {
				fmt.Fprintf(&b, " (%d runs)", c.Runs)
			}
			b.WriteString("\n")
		}
	}
	if w.Verify != "" {
		b.WriteString("\nLast verification: " + w.Verify + "\n")
	}
	if len(w.Todos) > 0 {
		b.WriteString("\nPlan still open (rewrite it with the todo tool as you go):\n")
		b.WriteString(renderOpenPlan(w.Todos))
	}
	section := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n" + title + ":\n")
		for i, s := range items {
			if title == "Next" {
				fmt.Fprintf(&b, "%d. %s\n", i+1, s)
			} else {
				b.WriteString("- " + s + "\n")
			}
		}
	}
	section("Decisions", w.Decisions)
	section("Ruled out", w.RuledOut)
	section("Facts about the codebase", w.Facts)
	section("Open questions", w.Open)
	section("Next", w.Next)
	if w.Notes != "" {
		b.WriteString("\nNotes:\n" + w.Notes + "\n")
	}
	b.WriteString("\n" + faultDiscipline + "\n")
	return strings.TrimRight(b.String(), "\n")
}

// faultDiscipline closes every compaction note. A summary reads as
// complete and confident, and a model that trusts it reconstructs the
// exact values it no longer has — paths, error strings, exit codes —
// plausibly and wrongly. The note must say what it is (a summary) and
// where the originals are (the archive, one expand_output call away).
const faultDiscipline = "This record is a summary; the exact text of earlier tool outputs is in the archive. When you need an exact value from before the compaction — a path, an error message, a command's output, a line of code — fetch it with expand_output (or re-read the file) rather than reconstructing it from memory, and say when you could not find it."
