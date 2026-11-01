package replay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Calls is how many model calls the recording holds.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.responses)
}

// Rebase rewrites every recorded reply so a path under from reads as the
// same path under to: tool-call arguments, text and thinking alike. A
// recording replayed in another directory than the one it ran in — a
// branched A/B arm in a fresh benchmark workspace — would otherwise send
// its absolute paths back to the original directory: over half the tool
// calls of the effbench r10–r13 janetcode sessions name the workspace by
// its absolute path. It returns how many blocks it changed.
//
// The rewrite is textual and anchored on the whole directory, so a path
// that merely shares a prefix with from (from + "-old") is left alone.
func (p *Provider) Rebase(from, to string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	from, to = strings.TrimRight(from, "/"), strings.TrimRight(to, "/")
	if from == "" || to == "" || from == to {
		return 0
	}
	// Inside JSON the paths are escaped the way json.Marshal escapes them.
	jfrom, jto := jsonInner(from), jsonInner(to)
	changed := 0
	for _, r := range p.responses {
		for i := range r.Message.Blocks {
			b := &r.Message.Blocks[i]
			var did bool
			if len(b.Input) > 0 {
				var in string
				in, did = rebaseText(string(b.Input), jfrom, jto)
				b.Input = json.RawMessage(in)
			}
			var t bool
			b.Text, t = rebaseText(b.Text, from, to)
			if did || t {
				changed++
			}
		}
	}
	return changed
}

// rebaseText replaces from with to wherever from is a whole directory:
// followed by a separator, a quote, whitespace or the end, never by more
// of a longer name, and not preceded by a path character, so the tail of
// a longer path (/x/home/u/proj for /home/u/proj) is left alone.
func rebaseText(s, from, to string) (string, bool) {
	if !strings.Contains(s, from) {
		return s, false
	}
	var b strings.Builder
	did := false
	for {
		i := strings.Index(s, from)
		if i < 0 {
			b.WriteString(s)
			break
		}
		end := i + len(from)
		before := i == 0 || !pathChar(s[i-1])
		if before && (end == len(s) || strings.ContainsRune("/\\\"' \t\n:;,)]}`", rune(s[end]))) {
			b.WriteString(s[:i])
			b.WriteString(to)
			did = true
		} else {
			b.WriteString(s[:end])
		}
		s = s[end:]
	}
	return b.String(), did
}

// pathChar reports whether c can sit inside a path name, so a match
// right after it is the middle of a longer path rather than its start.
func pathChar(c byte) bool {
	return c == '/' || c == '\\' || c == '.' || c == '-' || c == '_' ||
		'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9'
}

func jsonInner(s string) string {
	q, _ := json.Marshal(s)
	return string(q[1 : len(q)-1])
}

// RecordedCwd is the working directory a session file says its session
// ran in: the cwd of its meta record, the file's first line.
func RecordedCwd(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec struct {
			Type string `json:"type"`
			Meta struct {
				Cwd string `json:"cwd"`
			} `json:"meta"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Type == "meta" && rec.Meta.Cwd != "" {
			return rec.Meta.Cwd, nil
		}
		break
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return "", errors.New("replay: the session file has no meta record naming its working directory")
}
