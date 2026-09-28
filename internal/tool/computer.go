package tool

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Computer drives the local desktop: screenshot, click, type, key.
// It is always mutating so every call is permissioned, and it is
// deferred behind tool_search so its schema is not on every request.
type Computer struct {
	Root string
	// Exec, when set, is used instead of exec.CommandContext (tests).
	Exec func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func (Computer) Name() string { return "computer" }
func (Computer) Description() string {
	return "Drive the local desktop: screenshot, click, move, type, key. Always asks. Screenshot writes a PNG."
}
func (Computer) Mutating() bool { return true }
func (Computer) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","description":"screenshot, click, move, type, or key"},
"x":{"type":"integer"},
"y":{"type":"integer"},
"text":{"type":"string","description":"text to type"},
"key":{"type":"string","description":"key name: Return, Tab, Escape, ctrl+c"}},
"required":["action"],"additionalProperties":false}`)
}

func (t Computer) Guard(input json.RawMessage) (bool, string) {
	var in struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal(input, &in)
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "screenshot", "":
		return false, ""
	default:
		return true, "drives the local mouse and keyboard"
	}
}

func (t Computer) Preview(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action, Text, Key string
		X, Y              int
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "screenshot":
		return "take a screenshot of the desktop", nil
	case "click":
		return fmt.Sprintf("click at %d,%d", in.X, in.Y), nil
	case "move":
		return fmt.Sprintf("move the pointer to %d,%d", in.X, in.Y), nil
	case "type":
		return "type " + strconv.Quote(in.Text), nil
	case "key":
		return "press " + in.Key, nil
	default:
		return "computer " + in.Action, nil
	}
}

func (t Computer) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Action string `json:"action"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
		Text   string `json:"text"`
		Key    string `json:"key"`
	}
	if err := decode(input, &in); err != nil {
		return "", err
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	switch action {
	case "screenshot":
		return t.screenshot(ctx)
	case "click":
		return "", t.click(ctx, in.X, in.Y)
	case "move":
		return "", t.move(ctx, in.X, in.Y)
	case "type":
		if strings.TrimSpace(in.Text) == "" {
			return "", fmt.Errorf("type needs text")
		}
		return "", t.typeText(ctx, in.Text)
	case "key":
		if strings.TrimSpace(in.Key) == "" {
			return "", fmt.Errorf("key needs a key name")
		}
		return "", t.press(ctx, in.Key)
	default:
		return "", fmt.Errorf("action %q is not screenshot, click, move, type or key", in.Action)
	}
}

func (t Computer) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if t.Exec != nil {
		return t.Exec(ctx, name, args...)
	}
	// stdout alone is the result: grim and import write the PNG there, and
	// a warning on stderr mixed into it is a corrupt image. stderr is kept
	// for the error it explains.
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("%s: %w (%s)", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (t Computer) screenshot(ctx context.Context) (string, error) {
	dir := filepath.Join(t.Root, ".janetcode", "screenshots")
	if t.Root == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("shot-%d.png", os.Getpid()))
	var data []byte
	var err error
	switch runtime.GOOS {
	case "darwin":
		_, err = t.run(ctx, "screencapture", "-x", path)
		if err == nil {
			data, err = os.ReadFile(path)
		}
	default:
		data, err = t.run(ctx, "grim", "-")
		if err != nil {
			data, err = t.run(ctx, "import", "-window", "root", "png:-")
		}
		if err != nil {
			_, err = t.run(ctx, "scrot", "-o", path)
			if err == nil {
				data, err = os.ReadFile(path)
			}
		}
		if err == nil && len(data) > 0 {
			if werr := os.WriteFile(path, data, 0o644); werr != nil {
				return "", werr
			}
		}
	}
	if err != nil {
		return "", fmt.Errorf("screenshot: no capture tool worked (grim, import, scrot, or screencapture): %w", err)
	}
	w, h, _ := pngSize(data)
	rel := path
	if t.Root != "" {
		if r, rerr := filepath.Rel(t.Root, path); rerr == nil {
			rel = filepath.ToSlash(r)
		}
	}
	if w > 0 {
		return fmt.Sprintf("screenshot %dx%d saved at %s", w, h, rel), nil
	}
	return "screenshot saved at " + rel, nil
}

func (t Computer) click(ctx context.Context, x, y int) error {
	if runtime.GOOS == "darwin" {
		if _, err := t.run(ctx, "cliclick", fmt.Sprintf("c:%d,%d", x, y)); err == nil {
			return nil
		}
		script := fmt.Sprintf(`tell application "System Events" to click at {%d, %d}`, x, y)
		_, err := t.run(ctx, "osascript", "-e", script)
		return err
	}
	_, err := t.run(ctx, "xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", "1")
	return err
}

func (t Computer) move(ctx context.Context, x, y int) error {
	if runtime.GOOS == "darwin" {
		if _, err := t.run(ctx, "cliclick", fmt.Sprintf("m:%d,%d", x, y)); err == nil {
			return nil
		}
		return fmt.Errorf("move: install cliclick")
	}
	_, err := t.run(ctx, "xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y))
	return err
}

func (t Computer) typeText(ctx context.Context, text string) error {
	if runtime.GOOS == "darwin" {
		escaped := strings.ReplaceAll(text, `\`, `\\`)
		escaped = strings.ReplaceAll(escaped, `"`, `\"`)
		script := `tell application "System Events" to keystroke "` + escaped + `"`
		_, err := t.run(ctx, "osascript", "-e", script)
		return err
	}
	_, err := t.run(ctx, "xdotool", "type", "--", text)
	return err
}

func (t Computer) press(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if runtime.GOOS == "darwin" {
		script := `tell application "System Events" to keystroke "` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		if mapped := darwinKeystroke(key); mapped != "" {
			script = mapped
		}
		_, err := t.run(ctx, "osascript", "-e", script)
		return err
	}
	_, err := t.run(ctx, "xdotool", "key", xdotoolKey(key))
	return err
}

func darwinKeystroke(key string) string {
	// A chord such as ctrl+c: without this it fell through to keystroke
	// "ctrl+c", which types the six characters into the focused window.
	var using []string
	if parts := strings.Split(key, "+"); len(parts) > 1 && parts[len(parts)-1] != "" {
		for _, m := range parts[:len(parts)-1] {
			switch strings.ToLower(strings.TrimSpace(m)) {
			case "ctrl", "control":
				using = append(using, "control down")
			case "cmd", "command", "super", "meta":
				using = append(using, "command down")
			case "alt", "option", "opt":
				using = append(using, "option down")
			case "shift":
				using = append(using, "shift down")
			default:
				return ""
			}
		}
		key = strings.TrimSpace(parts[len(parts)-1])
	}
	var stroke string
	switch strings.ToLower(key) {
	case "return", "enter":
		stroke = `keystroke return`
	case "tab":
		stroke = `keystroke tab`
	case "escape", "esc":
		stroke = `key code 53`
	default:
		if len(using) == 0 || len([]rune(key)) != 1 {
			return ""
		}
		stroke = `keystroke "` + strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(key), `\`, `\\`), `"`, `\"`) + `"`
	}
	if len(using) > 0 {
		stroke += " using {" + strings.Join(using, ", ") + "}"
	}
	return `tell application "System Events" to ` + stroke
}

func xdotoolKey(key string) string {
	switch strings.ToLower(key) {
	case "return", "enter":
		return "Return"
	case "esc", "escape":
		return "Escape"
	case "tab":
		return "Tab"
	case "ctrl+c":
		return "ctrl+c"
	}
	return key
}

func pngSize(b []byte) (w, h int, ok bool) {
	if len(b) < 24 || !(len(b) > 8 && b[0] == 0x89 && string(b[1:4]) == "PNG") {
		return 0, 0, false
	}
	return int(binary.BigEndian.Uint32(b[16:20])), int(binary.BigEndian.Uint32(b[20:24])), true
}
