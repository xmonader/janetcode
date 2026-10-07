package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/trust"
)

// Editing .mcp.json in place. /mcp add writes the file the user would
// otherwise hand-edit, so it has to leave the rest of the file alone:
// other top-level keys, the order servers were written in, and the
// indentation already used all survive a write, and only the one entry
// being added or removed changes.

// ordered is a JSON object that remembers the order its keys were read
// in, so rewriting it does not shuffle the file.
type ordered struct {
	keys []string
	vals map[string]json.RawMessage
}

func newOrdered() *ordered { return &ordered{vals: map[string]json.RawMessage{}} }

// parseOrdered reads a JSON object, keeping key order and each value's
// raw bytes.
func parseOrdered(data []byte) (*ordered, error) {
	o := newOrdered()
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("expected a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected an object key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		o.set(key, raw)
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, err
	}
	return o, nil
}

func (o *ordered) set(key string, v json.RawMessage) {
	if _, had := o.vals[key]; !had {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *ordered) get(key string) (json.RawMessage, bool) {
	v, ok := o.vals[key]
	return v, ok
}

func (o *ordered) del(key string) bool {
	if _, ok := o.vals[key]; !ok {
		return false
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
	return true
}

// marshal renders the object with indent, nested depth levels deep.
func (o *ordered) marshal(indent string, depth int) ([]byte, error) {
	if len(o.keys) == 0 {
		return []byte("{}"), nil
	}
	pad := strings.Repeat(indent, depth+1)
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, k := range o.keys {
		key, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		b.WriteString(pad)
		b.Write(key)
		b.WriteString(": ")
		var v bytes.Buffer
		if err := json.Indent(&v, o.vals[k], pad, indent); err != nil {
			return nil, err
		}
		b.Write(v.Bytes())
		if i < len(o.keys)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat(indent, depth))
	b.WriteString("}")
	return b.Bytes(), nil
}

// sniffIndent finds the indentation the file already uses, so a
// four-space or tab-indented .mcp.json stays that way.
func sniffIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lead := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		if lead != "" {
			return lead
		}
	}
	return "  "
}

// ValidServerName rejects names that would not survive a round trip
// through the file, the tool names or a command line.
func ValidServerName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return errors.New("give the server a name")
	case name != strings.TrimSpace(name):
		return errors.New("a server name cannot start or end with a space")
	case strings.ContainsAny(name, " \t\n/\\\"'"):
		return errors.New("a server name cannot contain spaces, slashes or quotes")
	}
	return nil
}

// editProject applies fn to the mcpServers object of <cwd>/.mcp.json and
// writes the file back, creating it when absent. It returns the servers
// and the content hash as LoadProject would read them afterwards, so the
// caller can record trust for exactly what it wrote.
func editProject(cwd string, fn func(servers *ordered) error) (map[string]ServerConfig, string, error) {
	path := filepath.Join(cwd, ProjectFile)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		data = []byte("{}\n")
	case err != nil:
		return nil, "", err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}\n")
	}
	indent := sniffIndent(data)
	top, err := parseOrdered(data)
	if err != nil {
		return nil, "", fmt.Errorf("%s: %w", ProjectFile, err)
	}
	servers := newOrdered()
	if raw, ok := top.get("mcpServers"); ok && len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if servers, err = parseOrdered(raw); err != nil {
			return nil, "", fmt.Errorf("%s: mcpServers: %w", ProjectFile, err)
		}
	}
	if err := fn(servers); err != nil {
		return nil, "", err
	}
	body, err := servers.marshal(indent, 1)
	if err != nil {
		return nil, "", err
	}
	top.set("mcpServers", body)
	out, err := top.marshal(indent, 0)
	if err != nil {
		return nil, "", err
	}
	out = append(out, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return nil, "", err
	}
	trust.SelfWrote(cwd)
	return LoadProject(cwd)
}

// AddProjectServer writes cfg into <cwd>/.mcp.json under name, creating
// the file when it is missing and replacing an entry of the same name.
// The servers and content hash it returns are the file as it now stands.
func AddProjectServer(cwd, name string, cfg ServerConfig) (map[string]ServerConfig, string, error) {
	if err := ValidServerName(name); err != nil {
		return nil, "", err
	}
	entry, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	return editProject(cwd, func(servers *ordered) error {
		servers.set(name, entry)
		return nil
	})
}

// AddProjectServers writes several entries in one pass, for /mcp add
// --from, where a repository ships more than one server.
func AddProjectServers(cwd string, add map[string]ServerConfig, order []string) (map[string]ServerConfig, string, error) {
	for _, name := range order {
		if err := ValidServerName(name); err != nil {
			return nil, "", err
		}
	}
	return editProject(cwd, func(servers *ordered) error {
		for _, name := range order {
			entry, err := json.Marshal(add[name])
			if err != nil {
				return err
			}
			servers.set(name, entry)
		}
		return nil
	})
}

// RemoveProjectServer deletes name from <cwd>/.mcp.json. A name that is
// not in the file is an error, so /mcp remove can say so.
func RemoveProjectServer(cwd, name string) (map[string]ServerConfig, string, error) {
	return editProject(cwd, func(servers *ordered) error {
		if !servers.del(name) {
			return fmt.Errorf("no server named %q in %s", name, ProjectFile)
		}
		return nil
	})
}
