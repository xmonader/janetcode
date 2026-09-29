package shell

import "strings"

// StripHeredocs returns s with each here-document taken out: the <<word
// operator becomes a blank and the body, with its terminator line, is
// removed. A body is text fed to a command's stdin, not command lines,
// but a splitter that knows nothing of here-documents cuts it at its
// newlines and judges every line as a command — the commit message in
// `git commit -F - <<'EOF'` or in `git commit -m "$(cat <<'EOF' …)"`
// came out as commands no rule names. The command the here-document
// feeds stays, and is judged as it would be with its stdin from a file.
//
// It is conservative: when anything on the line is outside what it reads
// with certainty it returns s unchanged, and the line is judged line by
// line as before. That is the case for arithmetic ((…)), whose << is a
// shift; parameter expansions ${…} and backticks; a comment, whose << is
// not an operator; parentheses or quotes that do not balance (a `case`
// pattern's lone ")" among them); a delimiter that is empty or holds a
// $; a body that never meets its terminator; and an unquoted delimiter
// whose body holds $( or a backtick, which the shell would run.
func StripHeredocs(s string) string {
	if !strings.Contains(s, "<<") {
		return s
	}
	type heredoc struct {
		delim        string
		quoted, tabs bool
	}
	var (
		b       strings.Builder
		pending []heredoc
		// quotes is the quote state of each open $( … ) or ( … ), the
		// innermost last: a substitution starts unquoted even inside a
		// double-quoted string, and the string resumes when it closes.
		quotes = []byte{0}
	)
	wordStart := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		q := &quotes[len(quotes)-1]
		start := wordStart
		wordStart = false
		switch {
		case *q == '\'':
			b.WriteByte(c)
			if c == '\'' {
				*q = 0
			}
			continue
		case *q == '$':
			b.WriteByte(c)
			if c == '\\' && i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			} else if c == '\'' {
				*q = 0
			}
			continue
		case c == '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
			continue
		case c == '`', c == '$' && i+1 < len(s) && s[i+1] == '{':
			return s
		case c == '$' && strings.HasPrefix(s[i+1:], "(("), *q == 0 && strings.HasPrefix(s[i:], "(("):
			return s
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			b.WriteString("$(")
			i++
			quotes = append(quotes, 0)
			wordStart = true
			continue
		case *q == '"':
			b.WriteByte(c)
			if c == '"' {
				*q = 0
			}
			continue
		}
		// Unquoted from here on.
		switch {
		case c == '$' && i+1 < len(s) && s[i+1] == '\'':
			*q = '$'
			b.WriteString("$'")
			i++
		case c == '"' || c == '\'':
			*q = c
			b.WriteByte(c)
		case c == '#' && start:
			return s
		case c == '(':
			quotes = append(quotes, 0)
			b.WriteByte(c)
			wordStart = true
		case c == ')':
			if len(quotes) == 1 {
				return s
			}
			quotes = quotes[:len(quotes)-1]
			b.WriteByte(c)
		case c == '<' && strings.HasPrefix(s[i:], "<<<"):
			b.WriteString("<<<")
			i += 2
		case c == '<' && strings.HasPrefix(s[i:], "<<"):
			j := i + 2
			h := heredoc{}
			if j < len(s) && s[j] == '-' {
				h.tabs = true
				j++
			}
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				j++
			}
			k := j
			for k < len(s) && !strings.ContainsRune(" \t\n;&|<>()", rune(s[k])) {
				k++
			}
			word := s[j:k]
			if word == "" || strings.Contains(word, "$") {
				return s
			}
			h.quoted = strings.ContainsAny(word, `'"\`)
			h.delim = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(word)
			if h.delim == "" {
				return s
			}
			pending = append(pending, h)
			b.WriteByte(' ')
			i = k - 1
		case c == '\n' && len(pending) > 0:
			b.WriteByte('\n')
			rest := s[i+1:]
			consumed := 0
			for _, h := range pending {
				found := false
				for rest != "" {
					line, after, _ := strings.Cut(rest, "\n")
					n := len(line) + 1
					if after == "" && !strings.HasSuffix(rest, "\n") {
						n = len(line)
					}
					rest = rest[n:]
					consumed += n
					cmp := line
					if h.tabs {
						cmp = strings.TrimLeft(line, "\t")
					}
					if cmp == h.delim {
						found = true
						break
					}
					if !h.quoted && (strings.Contains(line, "$(") || strings.Contains(line, "`")) {
						return s
					}
				}
				if !found {
					return s
				}
			}
			pending = nil
			i += consumed
			wordStart = true
		default:
			b.WriteByte(c)
			wordStart = c == ' ' || c == '\t' || c == '\n' || c == ';' || c == '&' || c == '|'
		}
	}
	if len(pending) > 0 || len(quotes) != 1 || quotes[0] != 0 {
		return s
	}
	return b.String()
}
