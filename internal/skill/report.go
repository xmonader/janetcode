package skill

import (
	"fmt"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tokens"
)

// A skill is not free. Its one-line index entry is in the system prompt
// of every request for the whole session, and its body is sent whole
// whenever it matches. The listing used to name the skills and stop
// there, which told the user what they had and nothing about what it
// cost or whether it had ever been worth it. Row is that arithmetic:
// what one skill would cost if attached, what its index line costs
// every turn regardless, and what it has actually done this session.

// Row is one skill, priced.
type Row struct {
	Name        string
	Description string
	Source      string
	// Body is what attaching the skill costs, in tokens: the block the
	// model is sent, not the file on disk.
	Body int
	// Index is what its line in the system prompt's skill list costs, in
	// every request, whether it is ever attached or not.
	Index int
	// Used counts the times its text went into the context this session
	// (matched by the injector, brought in by a path rule, or read with
	// the skill tool); Injected says it is still in the context now.
	Used     int
	Injected bool
	// ModelInvocable is false for a skill only the user can invoke with
	// "/name": it costs nothing until it is typed.
	ModelInvocable bool
}

// Rows prices every skill, heaviest first. stats may be nil.
func Rows(skills []Skill, stats map[string]Stat) []Row {
	out := make([]Row, 0, len(skills))
	for _, s := range skills {
		r := Row{Name: s.Name, Description: s.Description, Source: s.Source,
			Body: tokens.Estimate(s.Block().Text), ModelInvocable: s.ModelInvocable}
		if s.ModelInvocable {
			r.Index = tokens.Estimate(fmt.Sprintf("- %s: %s\n", s.Name, s.Description))
		}
		if st, ok := stats[s.Name]; ok {
			r.Used, r.Injected = st.Used, st.Fresh
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Body != out[j].Body {
			return out[i].Body > out[j].Body
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Report renders the listing: every skill with what it costs, whether it
// is in the context now and whether it has earned its place this
// session, heaviest first, and a total against the request prefix.
// prefixTokens is the estimated size of the part of the request that is
// the same every turn (system prompt, project context, tool schemas);
// pass 0 when it is not known and the fraction is left out.
func Report(skills []Skill, stats map[string]Stat, prefixTokens int, where string) cmdresult.Result {
	if len(skills) == 0 {
		return cmdresult.Result{Plain: true, Sheet: "no skills found: " + where}
	}
	rows := Rows(skills, stats)
	var sb strings.Builder
	fmt.Fprintf(&sb, "%-22s %8s %7s  %-11s %s\n", "skill", "attached", "index", "state", "description")
	var body, index, used int
	for _, r := range rows {
		body += r.Body
		index += r.Index
		state := "unused"
		switch {
		case r.Injected:
			state = "in context"
		case r.Used > 0:
			state = fmt.Sprintf("used %d×", r.Used)
		case !r.ModelInvocable:
			state = "/name only"
		}
		if r.Used > 0 {
			used++
		}
		idx := textfmt.Tokens(r.Index)
		if r.Index == 0 {
			idx = "—"
		}
		fmt.Fprintf(&sb, "%-22s %8s %7s  %-11s %s\n", textfmt.Clip(r.Name, 22), textfmt.Tokens(r.Body), idx,
			state, textfmt.Clip(strings.TrimSpace(r.Description), 44))
	}
	fmt.Fprintf(&sb, "\n%s · %s of skill text if every one were attached · %s of index lines in every request",
		textfmt.Plural(len(rows), "skill"), textfmt.Tokens(body), textfmt.Tokens(index))
	if prefixTokens > 0 {
		fmt.Fprintf(&sb, " (%d%% of the %s prefix)", pct(index, prefixTokens), textfmt.Tokens(prefixTokens))
	}
	fmt.Fprintf(&sb, "\n%d of %d have been sent this session", used, len(rows))
	res := cmdresult.Result{Sheet: strings.TrimRight(sb.String(), "\n"), SheetTitle: "skills"}
	if used == 0 && len(rows) > 0 {
		res.Tips = append(res.Tips, "no skill has been used this session; the index lines above are paid for in every request whether one matches or not")
	}
	return res
}

func pct(n, total int) int {
	if total <= 0 {
		return 0
	}
	return n * 100 / total
}
