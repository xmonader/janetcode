package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// jval is a JSON value that remembers how it was written: object keys keep
// their order and scalars keep their exact bytes. A notebook is JSON that
// people diff and review, and every tool that writes one (Jupyter, VS
// Code, nbformat) has its own habits — key order, indent, whether
// non-ASCII is escaped, how a float is spelled. Decoding into
// map[string]any and marshalling back loses all of them, so a one-cell
// edit would show up as a rewrite of the whole file. This keeps what was
// not touched byte for byte.
type jval struct {
	kind byte // '{', '[', or 's' for any scalar (string, number, literal)
	keys []string
	vals []*jval
	raw  []byte // scalar bytes as written
}

func (v *jval) get(key string) *jval {
	if v == nil || v.kind != '{' {
		return nil
	}
	for i, k := range v.keys {
		if k == key {
			return v.vals[i]
		}
	}
	return nil
}

// set replaces key's value, or appends key at the end.
func (v *jval) set(key string, val *jval) {
	for i, k := range v.keys {
		if k == key {
			v.vals[i] = val
			return
		}
	}
	v.keys = append(v.keys, key)
	v.vals = append(v.vals, val)
}

// setSorted is set for a key that is new to an object whose keys are in
// sorted order, as nbformat writes them: the key goes where sorting puts
// it, so a new field does not stand out as the one out of place.
func (v *jval) setSorted(key string, val *jval) {
	for i, k := range v.keys {
		if k == key {
			v.vals[i] = val
			return
		}
	}
	at := len(v.keys)
	for i, k := range v.keys {
		if k > key {
			at = i
			break
		}
	}
	v.keys = append(v.keys[:at], append([]string{key}, v.keys[at:]...)...)
	v.vals = append(v.vals[:at], append([]*jval{val}, v.vals[at:]...)...)
}

func (v *jval) del(key string) {
	for i, k := range v.keys {
		if k == key {
			v.keys = append(v.keys[:i], v.keys[i+1:]...)
			v.vals = append(v.vals[:i], v.vals[i+1:]...)
			return
		}
	}
}

// str returns a string scalar's value; ok is false for anything else.
func (v *jval) str() (string, bool) {
	if v == nil || v.kind != 's' || len(v.raw) == 0 || v.raw[0] != '"' {
		return "", false
	}
	var s string
	if json.Unmarshal(v.raw, &s) != nil {
		return "", false
	}
	return s, true
}

// text reads a multiline field as nbformat stores it: a string, or a list
// of strings to be joined.
func (v *jval) text() string {
	if s, ok := v.str(); ok {
		return s
	}
	if v == nil || v.kind != '[' {
		return ""
	}
	var sb strings.Builder
	for _, e := range v.vals {
		s, _ := e.str()
		sb.WriteString(s)
	}
	return sb.String()
}

func rawScalar(s string) *jval { return &jval{kind: 's', raw: []byte(s)} }

// parseJSON reads one JSON document into a jval.
func parseJSON(data []byte) (*jval, error) {
	p := &jparser{b: data}
	p.ws()
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.i != len(p.b) {
		return nil, fmt.Errorf("unexpected data after JSON value at byte %d", p.i)
	}
	return v, nil
}

type jparser struct {
	b []byte
	i int
}

func (p *jparser) ws() {
	for p.i < len(p.b) {
		switch p.b[p.i] {
		case ' ', '\t', '\n', '\r':
			p.i++
		default:
			return
		}
	}
}

func (p *jparser) value() (*jval, error) {
	if p.i >= len(p.b) {
		return nil, fmt.Errorf("unexpected end of JSON")
	}
	switch c := p.b[p.i]; c {
	case '{':
		p.i++
		v := &jval{kind: '{'}
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == '}' {
			p.i++
			return v, nil
		}
		for {
			p.ws()
			ks, err := p.scalar()
			if err != nil {
				return nil, err
			}
			k, ok := ks.str()
			if !ok {
				return nil, fmt.Errorf("object key is not a string at byte %d", p.i)
			}
			p.ws()
			if p.i >= len(p.b) || p.b[p.i] != ':' {
				return nil, fmt.Errorf("expected ':' at byte %d", p.i)
			}
			p.i++
			p.ws()
			val, err := p.value()
			if err != nil {
				return nil, err
			}
			v.keys = append(v.keys, k)
			v.vals = append(v.vals, val)
			p.ws()
			if p.i >= len(p.b) {
				return nil, fmt.Errorf("unexpected end of JSON in object")
			}
			if p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.b[p.i] == '}' {
				p.i++
				return v, nil
			}
			return nil, fmt.Errorf("expected ',' or '}' at byte %d", p.i)
		}
	case '[':
		p.i++
		v := &jval{kind: '['}
		p.ws()
		if p.i < len(p.b) && p.b[p.i] == ']' {
			p.i++
			return v, nil
		}
		for {
			p.ws()
			val, err := p.value()
			if err != nil {
				return nil, err
			}
			v.vals = append(v.vals, val)
			p.ws()
			if p.i >= len(p.b) {
				return nil, fmt.Errorf("unexpected end of JSON in array")
			}
			if p.b[p.i] == ',' {
				p.i++
				continue
			}
			if p.b[p.i] == ']' {
				p.i++
				return v, nil
			}
			return nil, fmt.Errorf("expected ',' or ']' at byte %d", p.i)
		}
	default:
		return p.scalar()
	}
}

// scalar reads a string, number or literal, keeping its bytes, and checks
// it is valid JSON.
func (p *jparser) scalar() (*jval, error) {
	start := p.i
	if p.i < len(p.b) && p.b[p.i] == '"' {
		p.i++
		for p.i < len(p.b) {
			switch p.b[p.i] {
			case '\\':
				p.i += 2
				continue
			case '"':
				p.i++
				raw := p.b[start:p.i]
				if !json.Valid(raw) {
					return nil, fmt.Errorf("invalid string at byte %d", start)
				}
				return &jval{kind: 's', raw: raw}, nil
			}
			p.i++
		}
		return nil, fmt.Errorf("unterminated string at byte %d", start)
	}
	for p.i < len(p.b) && !strings.ContainsRune(",}] \t\r\n", rune(p.b[p.i])) {
		p.i++
	}
	raw := p.b[start:p.i]
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, fmt.Errorf("invalid JSON value at byte %d", start)
	}
	return &jval{kind: 's', raw: raw}, nil
}

// jstyle is how a JSON file was laid out, so it can be written back the
// same way.
type jstyle struct {
	indent   string // "" for a single-line file
	ascii    bool   // non-ASCII written as \u escapes
	trailing bool   // ends with a newline
}

func detectStyle(data []byte) jstyle {
	st := jstyle{trailing: bytes.HasSuffix(data, []byte("\n"))}
	if i := bytes.IndexByte(data, '\n'); i >= 0 && i+1 < len(data) {
		rest := data[i+1:]
		n := 0
		for n < len(rest) && (rest[n] == ' ' || rest[n] == '\t') {
			n++
		}
		st.indent = string(rest[:n])
		if st.indent == "" {
			st.indent = " "
		}
	}
	// A file with \u escapes and no raw non-ASCII byte was written with
	// ensure_ascii; new text is escaped the same way.
	st.ascii = bytes.Contains(data, []byte(`\u`)) && utf8.Valid(data) && !hasNonASCII(data)
	return st
}

func hasNonASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return true
		}
	}
	return false
}

// encodeString writes s as a JSON string in the file's style: raw UTF-8
// and no HTML escaping, as Python's json writes it, or \u escapes for a
// file written with ensure_ascii.
func (st jstyle) encodeString(s string) *jval {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	out := bytes.TrimRight(buf.Bytes(), "\n")
	if st.ascii {
		var sb strings.Builder
		for _, r := range string(out) {
			switch {
			case r < 0x80:
				sb.WriteRune(r)
			case r > 0xFFFF:
				r -= 0x10000
				fmt.Fprintf(&sb, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			default:
				fmt.Fprintf(&sb, `\u%04x`, r)
			}
		}
		out = []byte(sb.String())
	}
	return &jval{kind: 's', raw: append([]byte(nil), out...)}
}

// marshal writes v in the style: Python's json.dumps(indent=n) layout,
// which is what nbformat and Jupyter write.
func (st jstyle) marshal(v *jval) []byte {
	var buf bytes.Buffer
	st.write(&buf, v, 0)
	if st.trailing {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func (st jstyle) write(buf *bytes.Buffer, v *jval, depth int) {
	nl := func(d int) {
		if st.indent == "" {
			return
		}
		buf.WriteByte('\n')
		buf.WriteString(strings.Repeat(st.indent, d))
	}
	sep := ","
	colon := ":"
	if st.indent != "" {
		colon = ": "
	}
	switch v.kind {
	case '{':
		if len(v.keys) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				buf.WriteString(sep)
			}
			nl(depth + 1)
			buf.Write(st.encodeString(k).raw)
			buf.WriteString(colon)
			st.write(buf, v.vals[i], depth+1)
		}
		nl(depth)
		buf.WriteByte('}')
	case '[':
		if len(v.vals) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteByte('[')
		for i, e := range v.vals {
			if i > 0 {
				buf.WriteString(sep)
			}
			nl(depth + 1)
			st.write(buf, e, depth+1)
		}
		nl(depth)
		buf.WriteByte(']')
	default:
		buf.Write(v.raw)
	}
}
