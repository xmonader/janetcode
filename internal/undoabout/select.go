package undoabout

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/xmonader/janetcode/internal/change"
	"github.com/xmonader/janetcode/internal/terms"
	"math"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

const selectSystem = "You are selecting which of a coding session's file changes match a plain-language description, so the matching ones can be undone while the rest stay untouched. You are given the description and a numbered list of changes: each change's path, the model's stated reason for making it, how many lines it added and removed, and a few of the changed lines. Undo a change only when it clearly matches the description; when a change is unrelated, or you are not sure, keep it. One logical change can span several recorded changes across different files or calls — a rename touching both a definition and its call sites, for instance — treat those as a unit and put them all on the same side; never split one logical change across undo and keep. When a single recorded change itself mixes edits that match the description with edits that do not, do not guess which half to keep: list its number in \"mixed\" with why, instead of placing it in \"undo\" or \"keep\". Reply with JSON only, no prose outside it."

// undoSelectSchema constrains the model's reply the same way compaction's
// working-memory record does (see working.go): a provider that supports
// it is guaranteed to answer in this shape, one that does not is asked
// anyway and ignores the field, and the reply is parsed the same way
// either way.
var undoSelectSchema = &llm.Schema{
	Name:   "undo_selection",
	Strict: true,
	JSON: json.RawMessage(`{
"type":"object",
"properties":{
"undo":{"type":"array","items":{"type":"integer"}},
"keep":{"type":"array","items":{"type":"integer"}},
"mixed":{"type":"array","items":{"type":"object","properties":{"seq":{"type":"integer"},"why":{"type":"string"}},"required":["seq","why"],"additionalProperties":false}},
"why":{"type":"string"}},
"required":["undo","keep","mixed","why"],
"additionalProperties":false}`),
}

const (
	selectMaxHunksPerChange = 3
	selectMaxLinesPerHunk   = 4
	selectMaxPromptBytes    = 12_000
)

// selectByModel asks the model to split candidates against description.
func selectByModel(ctx context.Context, prov llm.Provider, model, cwd, description string, candidates []change.Change) (Selection, error) {
	prompt := buildSelectPrompt(cwd, description, candidates)
	resp, err := prov.Stream(ctx, llm.Request{
		Model:     model,
		System:    selectSystem,
		Messages:  []llm.Message{llm.TextMessage(llm.RoleUser, prompt)},
		MaxTokens: 1024,
		Schema:    undoSelectSchema,
	}, nil)
	if err != nil {
		return Selection{}, err
	}
	if resp == nil {
		return Selection{}, fmt.Errorf("empty reply")
	}
	undoIDs, keepIDs, mixedIDs, why, err := parseSelectionReply(resp.Message.Text())
	if err != nil {
		return Selection{}, err
	}
	return resolveSelection(candidates, undoIDs, keepIDs, mixedIDs, why), nil
}

// buildSelectPrompt renders description and candidates for selectByModel,
// capped so a long session cannot blow the call's budget: each change
// shows a few hunks, each hunk a few of its changed (not context) lines,
// and the whole prompt stops growing past selectMaxPromptBytes.
func buildSelectPrompt(cwd, description string, candidates []change.Change) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Description: %s\n\nCandidates:\n", description)
	for _, c := range candidates {
		add, del := c.Stats()
		fmt.Fprintf(&sb, "\n#%d turn %d %s +%d -%d", c.Seq, c.Turn, textfmt.RelTo(cwd, c.Path), add, del)
		if c.Intent != "" {
			fmt.Fprintf(&sb, " · %s", c.Intent)
		}
		sb.WriteString("\n")
		hunks := c.Hunks()
		for i, h := range hunks {
			if i >= selectMaxHunksPerChange {
				fmt.Fprintf(&sb, "  … %d more hunk(s)\n", len(hunks)-i)
				break
			}
			sb.WriteString("  " + h.Header() + "\n")
			shown := 0
			for _, l := range h.Lines {
				if l[0] == ' ' {
					continue
				}
				if shown >= selectMaxLinesPerHunk {
					sb.WriteString("    …\n")
					break
				}
				sb.WriteString("  " + strings.TrimRight(l, "\n") + "\n")
				shown++
			}
		}
		if sb.Len() > selectMaxPromptBytes {
			sb.WriteString("\n[more candidates omitted for size]\n")
			break
		}
	}
	sb.WriteString("\nReply with JSON {\"undo\":[...],\"keep\":[...],\"mixed\":[{\"seq\":n,\"why\":\"...\"}],\"why\":\"...\"} using the change numbers above. Keep a logical change that spans several recorded changes together on one side; list a change that mixes matching and unrelated edits in \"mixed\" instead of guessing which half to keep.")
	return sb.String()
}

// mixedSeq is one element of the model's "mixed" list: a change it could
// not cleanly place, and why.
type mixedSeq struct {
	Seq int    `json:"seq"`
	Why string `json:"why"`
}

// parseSelectionReply reads the model's JSON, tolerating a reply wrapped
// in a code fence or followed by trailing prose (see llm.ExtractJSONObject,
// which working.go's merge uses for the compaction record too).
func parseSelectionReply(text string) (undo, keep []int, mixed []mixedSeq, why string, err error) {
	var got struct {
		Undo  []int      `json:"undo"`
		Keep  []int      `json:"keep"`
		Mixed []mixedSeq `json:"mixed"`
		Why   string     `json:"why"`
	}
	if jerr := llm.ExtractJSONObject(text, &got); jerr != nil {
		return nil, nil, nil, "", jerr
	}
	return got.Undo, got.Keep, got.Mixed, strings.TrimSpace(got.Why), nil
}

// resolveSelection turns the model's (or the fallback's) seq numbers into
// Changes, silently dropping any seq that is not among candidates. A
// candidate the model named in none of the three lists is kept: undo is
// opt-in, not opt-out, so a name the model forgot to place does not get
// reverted.
func resolveSelection(candidates []change.Change, undoIDs, keepIDs []int, mixedIDs []mixedSeq, why string) Selection {
	byID := make(map[int]change.Change, len(candidates))
	for _, c := range candidates {
		byID[c.Seq] = c
	}
	var sel Selection
	placed := map[int]bool{}
	for _, id := range undoIDs {
		if c, ok := byID[id]; ok && !placed[id] {
			sel.Undo = append(sel.Undo, c)
			placed[id] = true
		}
	}
	for _, id := range keepIDs {
		if c, ok := byID[id]; ok && !placed[id] {
			sel.Keep = append(sel.Keep, c)
			placed[id] = true
		}
	}
	for _, m := range mixedIDs {
		if c, ok := byID[m.Seq]; ok && !placed[m.Seq] {
			sel.Mixed = append(sel.Mixed, Mixed{Change: c, Why: m.Why})
			placed[m.Seq] = true
		}
	}
	for _, c := range candidates {
		if !placed[c.Seq] {
			sel.Keep = append(sel.Keep, c)
		}
	}
	sel.Why = why
	return sel
}

// selectByKeywords is the fallback of last resort, used without a
// provider or when the model's reply could not be parsed: candidates are
// ranked against description by the same BM25 scoring searchArchive uses
// (see search.go), over each candidate's intent, path and the non-context
// lines of its diff. Keyword overlap is a much weaker signal than a model
// reading the description and the diff together, so a candidate only
// clears the bar when it scores close to the best match; everything else,
// including a candidate with no overlap at all, is kept.
func selectByKeywords(description string, candidates []change.Change) Selection {
	sel := Selection{Fallback: true}
	q := terms.Split(description)
	if len(q) == 0 {
		sel.Keep = append(sel.Keep, candidates...)
		sel.Why = "the description had no searchable words; nothing was matched"
		return sel
	}
	docs := make([]map[string]int, len(candidates))
	lens := make([]int, len(candidates))
	df := map[string]int{}
	total := 0
	for i, c := range candidates {
		tf := map[string]int{}
		n := 0
		for _, t := range terms.Split(changeText(c)) {
			tf[t]++
			n++
		}
		docs[i], lens[i] = tf, n
		total += n
		for t := range tf {
			df[t]++
		}
	}
	avg := math.Max(float64(total)/math.Max(float64(len(candidates)), 1), 1)
	scores := make([]float64, len(candidates))
	best := 0.0
	for i := range candidates {
		var score float64
		for _, t := range q {
			f := float64(docs[i][t])
			if f == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(candidates))-float64(df[t])+0.5)/(float64(df[t])+0.5))
			score += idf * (f * (terms.K1 + 1)) / (f + terms.K1*(1-terms.B+terms.B*float64(lens[i])/avg))
		}
		scores[i] = score
		if score > best {
			best = score
		}
	}
	if best <= 0 {
		sel.Keep = append(sel.Keep, candidates...)
		sel.Why = "no session change's path, reason or diff shares a word with the description; nothing was matched"
		return sel
	}
	const relThreshold = 0.6 // a candidate must score within 40% of the best to be undone
	for i, c := range candidates {
		if scores[i] >= best*relThreshold {
			sel.Undo = append(sel.Undo, c)
		} else {
			sel.Keep = append(sel.Keep, c)
		}
	}
	sel.Why = "matched by keyword overlap with the description (no model available to judge meaning)"
	return sel
}

// changeText is what selectByKeywords scores a change against: its
// stated reason, its path, and the lines its diff actually changed
// (context lines would only dilute the match).
func changeText(c change.Change) string {
	var sb strings.Builder
	sb.WriteString(c.Intent)
	sb.WriteString(" ")
	sb.WriteString(c.Path)
	for _, h := range c.Hunks() {
		for _, l := range h.Lines {
			if l[0] != ' ' {
				sb.WriteString(l)
			}
		}
	}
	return sb.String()
}

// Select matches description against candidates: with a provider, one
// side model call (not added to the conversation, the same discipline as
// AskOnce) is shown a compact numbered list and asked which to undo and
// which to keep; without one, or when the model's reply cannot be used, a
// BM25 keyword ranking over each candidate's intent, path and diff takes
// over and Selection.Fallback is set so the caller can say so.
//
// A candidate whose own edit can no longer be located is not a choice
// between undo and keep at all — offering it either way would promise
// something this guard cannot actually do. It is excluded before the
// model or the keyword fallback ever sees it (see partitionUndoable).
//
// cwd is the directory paths are shown relative to. prov may be nil.
func Select(ctx context.Context, prov llm.Provider, model, cwd, description string, all []change.Change) (Selection, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return Selection{}, fmt.Errorf("empty description")
	}
	if len(all) == 0 {
		return Selection{}, ErrNoChangesToUndo
	}
	candidates, unavailable := partitionUndoable(cwd, all)
	if len(candidates) == 0 {
		// Every change is still there but none can be reverted any more:
		// say which and why, rather than claiming there is nothing.
		return Selection{Unavailable: unavailable, Why: "every change left in this session was edited or reverted since"}, nil
	}
	sel, got := Selection{}, false
	if prov != nil {
		if s, err := selectByModel(ctx, prov, model, cwd, description, candidates); err == nil {
			sel, got = s, true
		}
	}
	if !got {
		sel = selectByKeywords(description, candidates)
	}
	sel.Unavailable = unavailable
	// The coupling guard runs regardless of how the split was made: a
	// model can miss it as easily as the keyword fallback can.
	sel.Warnings = detectCoupling(cwd, sel)
	return sel, nil
}

// Fingerprint identifies the set of undoable changes at a moment in time,
// cheaply enough to call on every preview and apply: it changes whenever
// a candidate is undone, added, or removed.
func Fingerprint(candidates []change.Change) string {
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = fmt.Sprintf("%d", c.Seq)
	}
	return strings.Join(ids, ",")
}
