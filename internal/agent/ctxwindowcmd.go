package agent

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/cmdresult"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Bounds for a chosen window: below the floor compaction could not keep
// the fixed prompt, above the ceiling no model exists.
const (
	MinContextWindow = 16_000
	MaxContextWindow = 10_000_000
)

// ParseWindow reads a context size: 200000, 200k, 1m, 1.5m. "default",
// "reset", "auto" and "off" give 0 (the model's own window).
func ParseWindow(arg string) (int, error) {
	arg = strings.ToLower(strings.TrimSpace(arg))
	switch arg {
	case "default", "reset", "auto", "off", "model":
		return 0, nil
	case "":
		return 0, fmt.Errorf("no size given")
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(arg, "k"):
		mult, arg = 1_000, strings.TrimSuffix(arg, "k")
	case strings.HasSuffix(arg, "m"):
		mult, arg = 1_000_000, strings.TrimSuffix(arg, "m")
	}
	f, err := strconv.ParseFloat(arg, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("not a size: use 200k, 1m, 500000 or default")
	}
	n := int(f*mult + 0.5)
	if n < MinContextWindow || n > MaxContextWindow {
		return 0, fmt.Errorf("window %s is outside %s to %s", textfmt.Tokens(n), textfmt.Tokens(MinContextWindow), textfmt.Tokens(MaxContextWindow))
	}
	return n, nil
}

// ContextWindowCommand is /context-window [size|default] in every
// front-end: it chooses the context window for this session, which sets
// where automatic compaction runs (three quarters of it, unless compact_at
// is set) and the denominator of the context meter. It never touches the
// config file and survives /model switches.
func (a *Agent) ContextWindowCommand(arg string) (cmdresult.Result, error) {
	cur := a.Settings()
	if strings.TrimSpace(arg) == "" {
		w := cur.EffectiveWindow()
		msg := "context window: the model's own is unknown · set one with /context-window <size>"
		if w > 0 {
			src := "the model's own"
			if cur.WindowOverride > 0 {
				src = "chosen for this session"
			}
			msg = fmt.Sprintf("context window %s (%s) · compaction at %s", textfmt.Tokens(w), src, textfmt.Tokens(a.CompactThreshold()))
		}
		return cmdresult.Result{Notes: []string{msg}, Tips: []string{"/context-window 200k, 1m or default · this session only"}}, nil
	}
	n, err := ParseWindow(arg)
	if err != nil {
		return cmdresult.Result{Error: "context-window: " + err.Error()}, nil
	}
	a.UpdateSettings(func(s *Settings) { s.WindowOverride = n })
	if n == 0 {
		return cmdresult.Result{Notes: []string{"context window back to the model's own"}}, nil
	}
	res := cmdresult.Result{Notes: []string{fmt.Sprintf("context window %s for this session · compaction at %s", textfmt.Tokens(n), textfmt.Tokens(a.CompactThreshold()))}}
	if cur.ContextWindow > 0 && n > cur.ContextWindow {
		res.Tips = []string{fmt.Sprintf("the model's own window is %s; a larger choice only helps if the provider really accepts it", textfmt.Tokens(cur.ContextWindow))}
	}
	return res, nil
}
