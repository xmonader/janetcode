// Package jsonschema checks a JSON value against a JSON Schema.
//
// It covers the part of the specification that schemas written for
// structured output actually use: types, properties and required,
// additionalProperties, items, enum and const, the numeric and length
// bounds, pattern, the anyOf/oneOf/allOf/not combinators, and local
// $ref pointers into $defs or definitions (what pydantic and zod emit).
// A keyword it does not know is ignored, as the specification says an
// unknown keyword must be; a $ref it cannot resolve is an error at
// Compile, so a schema is never half-checked without saying so.
package jsonschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schema is a compiled schema, ready to validate values.
type Schema struct {
	root     any
	patterns map[string]*regexp.Regexp
}

// Compile parses a schema document. It fails on JSON that does not parse,
// on a schema that is neither an object nor a boolean, on a pattern that
// is not a valid regular expression, and on a $ref it cannot resolve.
func Compile(doc []byte) (*Schema, error) {
	root, err := decode(doc)
	if err != nil {
		return nil, fmt.Errorf("schema is not valid JSON: %w", err)
	}
	switch root.(type) {
	case map[string]any, bool:
	default:
		return nil, fmt.Errorf("schema must be a JSON object, got %s", typeOf(root))
	}
	s := &Schema{root: root, patterns: map[string]*regexp.Regexp{}}
	if err := s.prepare(root, "#"); err != nil {
		return nil, err
	}
	return s, nil
}

// prepare walks the schema once, compiling patterns and resolving every
// $ref, so Validate never meets a schema error halfway through a value.
func (s *Schema) prepare(node any, at string) error {
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok {
			if _, err := s.resolve(ref); err != nil {
				return fmt.Errorf("%s: %w", at, err)
			}
		}
		if p, ok := n["pattern"].(string); ok {
			re, err := regexp.Compile(p)
			if err != nil {
				return fmt.Errorf("%s: pattern %q: %w", at, p, err)
			}
			s.patterns[p] = re
		}
		keys := make([]string, 0, len(n))
		for k := range n {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "enum" || k == "const" || k == "default" || k == "examples" {
				continue // values, not schemas
			}
			// These hold a map from a name to a schema. The names are
			// the user's, so a property called "default" is still a
			// schema: walk each one as a schema, not as keywords.
			if m, ok := n[k].(map[string]any); ok && schemaMaps[k] {
				names := make([]string, 0, len(m))
				for name := range m {
					names = append(names, name)
				}
				sort.Strings(names)
				for _, name := range names {
					if err := s.prepare(m[name], at+"/"+k+"/"+name); err != nil {
						return err
					}
				}
				continue
			}
			if err := s.prepare(n[k], at+"/"+k); err != nil {
				return err
			}
		}
	case []any:
		for i, v := range n {
			if err := s.prepare(v, at+"/"+strconv.Itoa(i)); err != nil {
				return err
			}
		}
	}
	return nil
}

// schemaMaps are the keywords whose value maps user-chosen names to schemas.
var schemaMaps = map[string]bool{
	"properties": true, "patternProperties": true, "$defs": true,
	"definitions": true, "dependentSchemas": true,
}

// resolve follows a local JSON pointer ("#", "#/$defs/Name").
func (s *Schema) resolve(ref string) (any, error) {
	if ref == "#" {
		return s.root, nil
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("$ref %q: only local references (#/...) are supported", ref)
	}
	var cur any = s.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			next, ok := c[part]
			if !ok {
				return nil, fmt.Errorf("$ref %q: %q not found", ref, part)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(c) {
				return nil, fmt.Errorf("$ref %q: bad index %q", ref, part)
			}
			cur = c[i]
		default:
			return nil, fmt.Errorf("$ref %q: cannot descend into %s", ref, typeOf(cur))
		}
	}
	return cur, nil
}

// Validate checks a JSON document against the schema. The error lists
// every violation found, one per line, each with the path to the value,
// so a model told what was wrong can fix all of it in one go.
func (s *Schema) Validate(doc []byte) error {
	v, err := decode(doc)
	if err != nil {
		return fmt.Errorf("not valid JSON: %w", err)
	}
	return s.ValidateValue(v)
}

// ValidateValue is Validate for an already-decoded value. Numbers must be
// json.Number, as decode produces them, for integer checks to be exact.
func (s *Schema) ValidateValue(v any) error {
	var errs []string
	s.check(s.root, v, "$", &errs, 0)
	if len(errs) == 0 {
		return nil
	}
	const max = 20
	if len(errs) > max {
		errs = append(errs[:max], fmt.Sprintf("... and %d more", len(errs)-max))
	}
	return fmt.Errorf("%s", strings.Join(errs, "\n"))
}

// maxDepth stops a self-referencing schema ({"$ref":"#"}) from looping
// on a value that never bottoms out in it.
const maxDepth = 64

func (s *Schema) check(node, v any, path string, errs *[]string, depth int) {
	if depth > maxDepth {
		*errs = append(*errs, path+": schema nesting too deep")
		return
	}
	switch n := node.(type) {
	case bool:
		if !n {
			*errs = append(*errs, path+": no value is allowed here")
		}
		return
	case map[string]any:
		s.checkObject(n, v, path, errs, depth)
	}
}

func (s *Schema) checkObject(n map[string]any, v any, path string, errs *[]string, depth int) {
	fail := func(format string, args ...any) {
		*errs = append(*errs, path+": "+fmt.Sprintf(format, args...))
	}
	if ref, ok := n["$ref"].(string); ok {
		target, _ := s.resolve(ref) // resolved at Compile
		s.check(target, v, path, errs, depth+1)
	}
	if t, ok := n["type"]; ok && !typeMatches(t, v) {
		fail("expected %s, got %s", typeList(t), typeOf(v))
		return // the remaining keywords would only repeat the mismatch
	}
	if e, ok := n["enum"].([]any); ok {
		found := false
		for _, want := range e {
			if equal(want, v) {
				found = true
				break
			}
		}
		if !found {
			fail("must be one of %s", compact(e))
		}
	}
	if c, ok := n["const"]; ok && !equal(c, v) {
		fail("must be %s", compact(c))
	}
	switch val := v.(type) {
	case string:
		l := float64(utf8.RuneCountInString(val))
		if m, ok := num(n["minLength"]); ok && l < m {
			fail("shorter than %v characters", m)
		}
		if m, ok := num(n["maxLength"]); ok && l > m {
			fail("longer than %v characters", m)
		}
		if p, ok := n["pattern"].(string); ok && !s.patterns[p].MatchString(val) {
			fail("does not match pattern %q", p)
		}
	case json.Number:
		f, _ := val.Float64()
		if m, ok := num(n["minimum"]); ok && f < m {
			fail("less than minimum %v", m)
		}
		if m, ok := num(n["maximum"]); ok && f > m {
			fail("greater than maximum %v", m)
		}
		if m, ok := num(n["exclusiveMinimum"]); ok && f <= m {
			fail("must be greater than %v", m)
		}
		if m, ok := num(n["exclusiveMaximum"]); ok && f >= m {
			fail("must be less than %v", m)
		}
		if m, ok := num(n["multipleOf"]); ok && m > 0 {
			if q := f / m; math.Abs(q-math.Round(q)) > 1e-9 {
				fail("not a multiple of %v", m)
			}
		}
	case []any:
		l := float64(len(val))
		if m, ok := num(n["minItems"]); ok && l < m {
			fail("fewer than %v items", m)
		}
		if m, ok := num(n["maxItems"]); ok && l > m {
			fail("more than %v items", m)
		}
		if u, _ := n["uniqueItems"].(bool); u {
			for i := range val {
				for j := i + 1; j < len(val); j++ {
					if equal(val[i], val[j]) {
						fail("items %d and %d are equal", i, j)
					}
				}
			}
		}
		prefix, _ := n["prefixItems"].([]any)
		for i, item := range val {
			p := fmt.Sprintf("%s[%d]", path, i)
			if i < len(prefix) {
				s.check(prefix[i], item, p, errs, depth+1)
			} else if items, ok := n["items"]; ok {
				s.check(items, item, p, errs, depth+1)
			}
		}
	case map[string]any:
		l := float64(len(val))
		if m, ok := num(n["minProperties"]); ok && l < m {
			fail("fewer than %v properties", m)
		}
		if m, ok := num(n["maxProperties"]); ok && l > m {
			fail("more than %v properties", m)
		}
		if req, ok := n["required"].([]any); ok {
			for _, r := range req {
				if name, ok := r.(string); ok {
					if _, present := val[name]; !present {
						fail("missing required property %q", name)
					}
				}
			}
		}
		props, _ := n["properties"].(map[string]any)
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			p := path + "." + k
			if ps, ok := props[k]; ok {
				s.check(ps, val[k], p, errs, depth+1)
				continue
			}
			if ap, ok := n["additionalProperties"]; ok {
				if b, isBool := ap.(bool); isBool && !b {
					fail("unexpected property %q", k)
					continue
				}
				s.check(ap, val[k], p, errs, depth+1)
			}
		}
	}
	if all, ok := n["allOf"].([]any); ok {
		for _, sub := range all {
			s.check(sub, v, path, errs, depth+1)
		}
	}
	if anyOf, ok := n["anyOf"].([]any); ok && s.matching(anyOf, v, path, depth) == 0 {
		fail("matches none of the anyOf alternatives")
	}
	if oneOf, ok := n["oneOf"].([]any); ok {
		if c := s.matching(oneOf, v, path, depth); c != 1 {
			fail("must match exactly one oneOf alternative, matches %d", c)
		}
	}
	if not, ok := n["not"]; ok {
		var sub []string
		s.check(not, v, path, &sub, depth+1)
		if len(sub) == 0 {
			fail("must not match the \"not\" schema")
		}
	}
}

// matching counts the alternatives v satisfies.
func (s *Schema) matching(alts []any, v any, path string, depth int) int {
	c := 0
	for _, a := range alts {
		var sub []string
		s.check(a, v, path, &sub, depth+1)
		if len(sub) == 0 {
			c++
		}
	}
	return c
}

func typeMatches(t, v any) bool {
	switch tt := t.(type) {
	case string:
		return isType(tt, v)
	case []any:
		for _, x := range tt {
			if name, ok := x.(string); ok && isType(name, v) {
				return true
			}
		}
		return false
	}
	return true
}

func isType(name string, v any) bool {
	switch name {
	case "integer":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		if _, err := n.Int64(); err == nil {
			return true
		}
		f, err := n.Float64()
		return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
	case "number":
		_, ok := v.(json.Number)
		return ok
	}
	return typeOf(v) == name
}

func typeList(t any) string {
	if xs, ok := t.([]any); ok {
		parts := make([]string, 0, len(xs))
		for _, x := range xs {
			parts = append(parts, fmt.Sprint(x))
		}
		return strings.Join(parts, " or ")
	}
	return fmt.Sprint(t)
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

func num(v any) (float64, bool) {
	if n, ok := v.(json.Number); ok {
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// equal compares two decoded JSON values; numbers compare by value, so
// 1 and 1.0 are equal as JSON Schema says they are.
func equal(a, b any) bool {
	if na, ok := a.(json.Number); ok {
		nb, ok := b.(json.Number)
		if !ok {
			return false
		}
		fa, ea := na.Float64()
		fb, eb := nb.Float64()
		return ea == nil && eb == nil && fa == fb
	}
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !equal(xv, yv) {
				return false
			}
		}
		return true
	}
	return a == b
}

func compact(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// decode reads exactly one JSON value, keeping numbers exact.
func decode(doc []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return v, nil
}
