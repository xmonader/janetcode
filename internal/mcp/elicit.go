package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on a server-supplied form, so a hostile or buggy server cannot
// make the client render an unbounded dialog (the same bounds grok uses).
const (
	MaxElicitFields    = 32
	MaxElicitMessage   = 4096
	MaxElicitName      = 64
	MaxElicitTitle     = 128
	MaxElicitDesc      = 512
	MaxElicitEnum      = 32
	MaxElicitEnumValue = 128
	MaxElicitSchema    = 64 * 1024
	MaxElicitDraft     = 4096
)

// ElicitRequest is a server's elicitation/create asking the user for
// structured input (MCP 2025-06-18, form mode).
type ElicitRequest struct {
	Server  string
	Message string
	Schema  json.RawMessage // the requestedSchema, unparsed
}

// ElicitResponse is the user's answer: Action is accept, decline or
// cancel, and Content (accept only) is the validated form.
type ElicitResponse struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// Elicit actions, as the protocol spells them.
const (
	ElicitAccept  = "accept"
	ElicitDecline = "decline"
	ElicitCancel  = "cancel"
)

// ElicitTimeout bounds how long a server's request waits for the user
// before it is cancelled.
const ElicitTimeout = 5 * time.Minute

// FieldKind is the input a form field takes.
type FieldKind string

const (
	KindString  FieldKind = "string"
	KindNumber  FieldKind = "number"
	KindInteger FieldKind = "integer"
	KindBoolean FieldKind = "boolean"
	KindSelect  FieldKind = "select"
	KindMulti   FieldKind = "multiselect"
)

// Option is one choice of a select field.
type Option struct{ Value, Label string }

// Field is one parsed property of the requested schema.
type Field struct {
	Name, Title, Description string
	Required                 bool
	Kind                     FieldKind
	Format                   string // string: email, uri, date, date-time
	MinLength, MaxLength     *int
	Min, Max                 *float64
	MinItems, MaxItems       *int
	Options                  []Option
	Default                  string
}

func within(s string, n int) bool { return utf8.RuneCountInString(s) <= n }

// ParseForm turns a requestedSchema into ordered fields. Only the flat
// object of primitives the spec allows is accepted; anything else is an
// error the server is told about.
func ParseForm(raw json.RawMessage) ([]Field, error) {
	if len(raw) > MaxElicitSchema {
		return nil, fmt.Errorf("requestedSchema exceeds %d bytes", MaxElicitSchema)
	}
	var top struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("requestedSchema must be a JSON object")
	}
	if top.Type != "" && top.Type != "object" {
		return nil, fmt.Errorf(`requestedSchema.type must be "object"`)
	}
	if top.Properties == nil {
		return nil, fmt.Errorf("requestedSchema.properties is required")
	}
	if len(top.Properties) > MaxElicitFields {
		return nil, fmt.Errorf("requestedSchema.properties exceeds %d fields", MaxElicitFields)
	}
	req := map[string]bool{}
	for _, r := range top.Required {
		req[r] = true
	}
	var fields []Field
	for _, name := range propertyOrder(raw) {
		p, ok := top.Properties[name]
		if !ok {
			continue
		}
		if !within(name, MaxElicitName) {
			return nil, fmt.Errorf("property name exceeds %d characters", MaxElicitName)
		}
		f, err := parseField(name, p, req[name])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		fields = append(fields, f)
	}
	return fields, nil
}

// propertyOrder lists the keys of "properties" in document order, which
// a Go map forgets and a form must keep.
func propertyOrder(raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var keys []string
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return keys
		}
		if kt != "properties" {
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return keys
			}
			continue
		}
		if t, err := dec.Token(); err != nil || t != json.Delim('{') {
			return keys
		}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return keys
			}
			keys = append(keys, k.(string))
			var skip json.RawMessage
			if dec.Decode(&skip) != nil {
				return keys
			}
		}
		return keys
	}
	return keys
}

type propSchema struct {
	Type        string            `json:"type"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Default     json.RawMessage   `json:"default"`
	Format      string            `json:"format"`
	MinLength   *int              `json:"minLength"`
	MaxLength   *int              `json:"maxLength"`
	Minimum     *float64          `json:"minimum"`
	Maximum     *float64          `json:"maximum"`
	MinItems    *int              `json:"minItems"`
	MaxItems    *int              `json:"maxItems"`
	Enum        []json.RawMessage `json:"enum"`
	EnumNames   []string          `json:"enumNames"`
	OneOf       []constTitle      `json:"oneOf"`
	AnyOf       []constTitle      `json:"anyOf"`
	Items       *struct {
		Enum  []json.RawMessage `json:"enum"`
		AnyOf []constTitle      `json:"anyOf"`
		OneOf []constTitle      `json:"oneOf"`
	} `json:"items"`
}

type constTitle struct {
	Const json.RawMessage `json:"const"`
	Title string          `json:"title"`
}

func scalar(raw json.RawMessage) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func parseField(name string, raw json.RawMessage, required bool) (Field, error) {
	var p propSchema
	if err := json.Unmarshal(raw, &p); err != nil {
		return Field{}, fmt.Errorf("not a valid property schema")
	}
	f := Field{Name: name, Title: p.Title, Description: p.Description, Required: required}
	if f.Title == "" {
		f.Title = name
	}
	if !within(f.Title, MaxElicitTitle) {
		return f, fmt.Errorf("title exceeds %d characters", MaxElicitTitle)
	}
	if !within(f.Description, MaxElicitDesc) {
		return f, fmt.Errorf("description exceeds %d characters", MaxElicitDesc)
	}
	if len(p.Default) > 0 {
		if d, ok := scalar(p.Default); ok {
			if !within(d, MaxElicitDraft) {
				return f, fmt.Errorf("default exceeds %d characters", MaxElicitDraft)
			}
			f.Default = d
		}
	}
	opts := func(enum []json.RawMessage, names []string, titled []constTitle) ([]Option, error) {
		var out []Option
		for i, e := range enum {
			if v, ok := scalar(e); ok {
				l := v
				if i < len(names) && names[i] != "" {
					l = names[i]
				}
				out = append(out, Option{v, l})
			}
		}
		for _, t := range titled {
			if v, ok := scalar(t.Const); ok {
				l := t.Title
				if l == "" {
					l = v
				}
				out = append(out, Option{v, l})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("enum has no usable values")
		}
		if len(out) > MaxElicitEnum {
			return nil, fmt.Errorf("enum exceeds %d values", MaxElicitEnum)
		}
		for _, o := range out {
			if !within(o.Value, MaxElicitEnumValue) || !within(o.Label, MaxElicitEnumValue) {
				return nil, fmt.Errorf("an enum value exceeds %d characters", MaxElicitEnumValue)
			}
		}
		return out, nil
	}
	var err error
	switch {
	case len(p.Enum) > 0 || len(p.OneOf) > 0:
		f.Kind = KindSelect
		f.Options, err = opts(p.Enum, p.EnumNames, p.OneOf)
	case p.Type == "array":
		f.Kind = KindMulti
		f.MinItems, f.MaxItems = p.MinItems, p.MaxItems
		if p.Items == nil {
			return f, fmt.Errorf("array fields need items with an enum")
		}
		f.Options, err = opts(p.Items.Enum, nil, append(append([]constTitle(nil), p.Items.AnyOf...), p.Items.OneOf...))
	case p.Type == "boolean":
		f.Kind = KindBoolean
	case p.Type == "integer":
		f.Kind = KindInteger
		f.Min, f.Max = p.Minimum, p.Maximum
	case p.Type == "number":
		f.Kind = KindNumber
		f.Min, f.Max = p.Minimum, p.Maximum
	case p.Type == "string" || p.Type == "":
		f.Kind = KindString
		f.Format, f.MinLength, f.MaxLength = p.Format, p.MinLength, p.MaxLength
	default:
		return f, fmt.Errorf("unsupported type %q", p.Type)
	}
	return f, err
}

// Validate checks what the user entered for f and returns the JSON value
// to send. present is false when the field is optional and was left empty.
// input is the typed text; for select fields it is an option label (or
// value), for multi-select a comma-separated list of them, for booleans
// yes/no/true/false.
func (f Field) Validate(input string) (val any, present bool, err error) {
	empty := func() (any, bool, error) {
		if f.Required {
			return nil, false, fmt.Errorf("required")
		}
		return nil, false, nil
	}
	switch f.Kind {
	case KindBoolean:
		switch strings.ToLower(strings.TrimSpace(input)) {
		case "":
			if f.Default != "" {
				return f.Default == "true", true, nil
			}
			return empty()
		case "yes", "y", "true", "on", "1":
			return true, true, nil
		case "no", "n", "false", "off", "0":
			return false, true, nil
		}
		return nil, false, fmt.Errorf("answer yes or no")
	case KindSelect:
		if strings.TrimSpace(input) == "" {
			return empty()
		}
		if o, ok := f.find(input); ok {
			return o.Value, true, nil
		}
		return nil, false, fmt.Errorf("not one of the choices")
	case KindMulti:
		var vals []string
		for _, part := range strings.Split(input, ",") {
			if strings.TrimSpace(part) == "" {
				continue
			}
			o, ok := f.find(part)
			if !ok {
				return nil, false, fmt.Errorf("%q is not one of the choices", strings.TrimSpace(part))
			}
			vals = append(vals, o.Value)
		}
		if f.MinItems != nil && len(vals) < *f.MinItems {
			return nil, false, fmt.Errorf("select at least %d", *f.MinItems)
		}
		if f.MaxItems != nil && len(vals) > *f.MaxItems {
			return nil, false, fmt.Errorf("select at most %d", *f.MaxItems)
		}
		if len(vals) == 0 && !f.Required {
			return nil, false, nil
		}
		if vals == nil {
			vals = []string{}
		}
		return vals, true, nil
	case KindString:
		if input == "" {
			return empty()
		}
		n := utf8.RuneCountInString(input)
		if f.MinLength != nil && n < *f.MinLength {
			return nil, false, fmt.Errorf("min length %d", *f.MinLength)
		}
		if f.MaxLength != nil && n > *f.MaxLength {
			return nil, false, fmt.Errorf("max length %d", *f.MaxLength)
		}
		if msg := textFormat(f.Format, input); msg != "" {
			return nil, false, fmt.Errorf("%s", msg)
		}
		return input, true, nil
	case KindInteger, KindNumber:
		s := strings.TrimSpace(input)
		if s == "" {
			return empty()
		}
		if f.Kind == KindInteger {
			n, perr := strconv.ParseInt(s, 10, 64)
			if perr != nil {
				return nil, false, fmt.Errorf("must be an integer")
			}
			if f.Min != nil && float64(n) < *f.Min {
				return nil, false, fmt.Errorf("min %g", *f.Min)
			}
			if f.Max != nil && float64(n) > *f.Max {
				return nil, false, fmt.Errorf("max %g", *f.Max)
			}
			return n, true, nil
		}
		n, perr := strconv.ParseFloat(s, 64)
		if perr != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			return nil, false, fmt.Errorf("invalid number")
		}
		if f.Min != nil && n < *f.Min {
			return nil, false, fmt.Errorf("min %g", *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return nil, false, fmt.Errorf("max %g", *f.Max)
		}
		return n, true, nil
	}
	return nil, false, fmt.Errorf("unsupported field")
}

func (f Field) find(s string) (Option, bool) {
	s = strings.TrimSpace(s)
	for _, o := range f.Options {
		if o.Label == s || o.Value == s {
			return o, true
		}
	}
	for _, o := range f.Options {
		if strings.EqualFold(o.Label, s) || strings.EqualFold(o.Value, s) {
			return o, true
		}
	}
	return Option{}, false
}

func textFormat(format, s string) string {
	switch format {
	case "email":
		if a, err := mail.ParseAddress(s); err != nil || a.Address != s {
			return "not an email address"
		}
	case "uri":
		if u, err := url.Parse(s); err != nil || u.Scheme == "" {
			return "not a URI"
		}
	case "date":
		if _, err := time.Parse("2006-01-02", s); err != nil {
			return "use YYYY-MM-DD"
		}
	case "date-time":
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			return "use RFC 3339, e.g. 2026-01-02T15:04:05Z"
		}
	}
	return ""
}

// handleElicit answers a server's elicitation/create. Without a handler
// (OnElicit unset) the capability was never advertised, so the request is
// an error; a bad schema is an invalid-params error the server can read.
func (c *Client) handleElicit(raw json.RawMessage) (json.RawMessage, *rpcError) {
	if c.onElicit == nil {
		return nil, &rpcError{Code: -32601, Message: "elicitation is not available in this session"}
	}
	var p struct {
		Mode            string          `json:"mode"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "bad elicitation params"}
	}
	if p.Mode != "" && p.Mode != "form" {
		return nil, &rpcError{Code: -32602, Message: "only form-mode elicitation is supported"}
	}
	if !within(p.Message, MaxElicitMessage) {
		return nil, &rpcError{Code: -32602, Message: fmt.Sprintf("message exceeds %d characters", MaxElicitMessage)}
	}
	if _, err := ParseForm(p.RequestedSchema); err != nil {
		return nil, &rpcError{Code: -32602, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), ElicitTimeout)
	defer cancel()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	resp := c.onElicit(ctx, ElicitRequest{Server: c.Name, Message: p.Message, Schema: p.RequestedSchema})
	if resp.Action != ElicitAccept && resp.Action != ElicitDecline {
		resp = ElicitResponse{Action: ElicitCancel}
	}
	if resp.Action != ElicitAccept {
		resp.Content = nil
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return out, nil
}
