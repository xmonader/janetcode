package effbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/shell"
)

// Result is a run's result.json. Token numbers are not stored here: the
// report recomputes them from meter.jsonl, the independent record.
type Result struct {
	Campaign string  `json:"campaign"`
	Arm      string  `json:"arm"`
	Task     string  `json:"task"`
	Seed     int     `json:"seed"`
	Model    string  `json:"model"`
	Pass     bool    `json:"pass"`
	WallSec  float64 `json:"wall_sec"`
	Exit     int     `json:"exit"`
	TimedOut bool    `json:"timed_out,omitempty"`
	Skipped  string  `json:"skipped,omitempty"` // why the run did not happen
	Error    string  `json:"error,omitempty"`
}

// Supported reports why a task cannot run on a foreign CLI ("" if it can).
// Sessions and repo-based tasks need janetcode's own runner.
func Supported(t eval.Task) string {
	switch {
	case t.Session():
		return "session task"
	case t.Repo != "" || t.Patch != "":
		return "repo/patch task"
	}
	return ""
}

// Config is one campaign.
type Config struct {
	Out        string // campaign directory
	Campaign   string
	Model      string
	Seeds      int
	Timeout    time.Duration
	Adapters   []Adapter
	Tasks      []eval.Task
	Upstream   string
	Key        string
	Client     *http.Client
	JanetBin   string // overrides the janetcode adapter binary when set
	DryRun     bool   // plan only
	Logf       func(string, ...any)
	KeepWS     bool
	RunnerHook func(ctx context.Context, a Adapter, v Vars, ws string) error // test seam: replaces exec
}

func (c *Config) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

// RunDir is the directory of one run.
func (c *Config) RunDir(arm, task string, seed int) string {
	return filepath.Join(c.Out, c.Campaign, arm, fmt.Sprintf("%s-s%d", task, seed))
}

// Execute runs every available adapter over every supported task and seed.
// Missing binaries and unsupported tasks are recorded as skipped.
func (c *Config) Execute(ctx context.Context) ([]Result, error) {
	var out []Result
	for _, a := range c.Adapters {
		if a.Name == "janetcode" && c.JanetBin != "" {
			a.Bin = c.JanetBin
		}
		if _, ok := a.Available(); !ok && c.RunnerHook == nil {
			c.logf("skip %s: %q not installed", a.Name, a.Bin)
			out = append(out, Result{Campaign: c.Campaign, Arm: a.Name, Model: c.Model, Skipped: "binary not found: " + a.Bin})
			continue
		}
		for _, t := range c.Tasks {
			if why := Supported(t); why != "" {
				c.logf("skip %s/%s: %s", a.Name, t.Name, why)
				continue
			}
			for s := 1; s <= c.Seeds; s++ {
				if ctx.Err() != nil {
					return out, ctx.Err()
				}
				if c.DryRun {
					c.logf("plan %s %s seed %d -> %s", a.Name, t.Name, s, c.RunDir(a.Name, t.Name, s))
					continue
				}
				r := c.one(ctx, a, t, s)
				c.logf("%s %s s%d pass=%v wall=%.0fs", a.Name, t.Name, s, r.Pass, r.WallSec)
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (c *Config) one(ctx context.Context, a Adapter, t eval.Task, seed int) (res Result) {
	dir := c.RunDir(a.Name, t.Name, seed)
	res = Result{Campaign: c.Campaign, Arm: a.Name, Task: t.Name, Seed: seed, Model: c.Model}
	fail := func(err error) Result {
		res.Error = err.Error()
		writeJSON(filepath.Join(dir, "result.json"), res)
		return res
	}
	ws := filepath.Join(dir, "ws")
	home := filepath.Join(dir, "home")
	for _, d := range []string{ws, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(err)
		}
	}
	if err := writeTree(ws, t.Files); err != nil {
		return fail(err)
	}
	mf, err := os.Create(filepath.Join(dir, "meter.jsonl"))
	if err != nil {
		return fail(err)
	}
	defer mf.Close()
	log := NewLog(mf)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fail(err)
	}
	srv := &http.Server{Handler: &Proxy{Upstream: c.Upstream, Key: c.Key, Log: log, Client: c.Client}}
	go srv.Serve(ln)
	defer srv.Close()
	proxyURL := "http://" + ln.Addr().String()

	prompt := t.Prompt
	vars := Vars{BaseURL: a.BaseURL(proxyURL), Model: c.Model, Prompt: prompt, Key: "effbench", WS: ws, Home: home}
	outPath := filepath.Join(dir, "answer.txt")
	start := time.Now()
	if c.RunnerHook != nil {
		err = c.RunnerHook(ctx, a, vars, ws)
	} else {
		res.Exit, res.TimedOut, err = c.exec(ctx, a, vars, ws, dir, outPath)
	}
	res.WallSec = time.Since(start).Seconds()
	if err != nil {
		res.Error = err.Error()
	}
	res.Pass = Grade(ctx, t, ws, dir, outPath)
	if !c.KeepWS {
		_ = os.RemoveAll(ws)
	}
	_ = os.RemoveAll(home)
	writeJSON(filepath.Join(dir, "result.json"), res)
	return res
}

func (c *Config) exec(ctx context.Context, a Adapter, v Vars, ws, dir, outPath string) (exit int, timedOut bool, err error) {
	argv, env, files := a.Command(v)
	if err := writeTree(v.Home, files); err != nil {
		return 0, false, err
	}
	bin, _ := a.Available()
	cctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, argv[1:]...)
	shell.Supervise(cmd)
	cmd.Dir = ws
	// Scrubbed: no agent may find a second provider's credentials.
	cmd.Env = append(shell.ScrubEnv(os.Environ()), "HOME="+v.Home)
	cmd.Env = append(cmd.Env, env...)
	if a.Stdin {
		cmd.Stdin = strings.NewReader(v.Prompt)
	}
	of, ferr := os.Create(outPath)
	if ferr != nil {
		return 0, false, ferr
	}
	defer of.Close()
	cmd.Stdout = of
	ef, _ := os.Create(filepath.Join(dir, "agent.err"))
	if ef != nil {
		defer ef.Close()
		cmd.Stderr = ef
	}
	err = cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return -1, true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), false, nil
	}
	return 0, false, err
}

func writeTree(root string, files map[string]string) error {
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeJSON(path string, v any) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if b, err := json.MarshalIndent(v, "", "  "); err == nil {
		_ = os.WriteFile(path, append(b, '\n'), 0o644)
	}
}

// Grade runs the task's own check, the same one `janetcode --eval` uses. Locked
// files are restored first. Held-out tests are laid over a copy of the
// workspace only now, after the agent has finished, so no agent can read them.
func Grade(ctx context.Context, t eval.Task, ws, dir, outPath string) bool {
	if err := writeTree(ws, lockedFiles(t)); err != nil {
		return false
	}
	gate := ""
	if t.Hidden() {
		hd := filepath.Join(dir, "heldout")
		defer os.RemoveAll(hd)
		tests := map[string]string{}
		for n, b := range t.HiddenFiles {
			tests[filepath.Join(eval.HiddenDir, n)] = b
		}
		if writeTree(filepath.Join(hd, "tests"), tests) != nil {
			return false
		}
		hc := strings.TrimSpace(t.HiddenCheck)
		if hc == "" {
			hc = "go test ./" + eval.HiddenDir + "/"
		}
		gate = filepath.Join(hd, "gate.sh")
		script := `#!/bin/sh
H=$(cd "$(dirname "$0")" && pwd)
S="$H/scratch"; rm -rf "$S"; mkdir -p "$S"
(cd "$1" && tar cf - .) | (cd "$S" && tar xf -) || exit 2
rm -rf "$S/` + eval.HiddenDir + `"
(cd "$H/tests" && tar cf - .) | (cd "$S" && tar xf -) || exit 2
cd "$S" && { ` + hc + ` ; }
`
		if os.WriteFile(gate, []byte(script), 0o700) != nil {
			return false
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := shell.Command(cctx, t.Check)
	shell.Supervise(cmd)
	cmd.Dir = ws
	cmd.Env = append(shell.ScrubEnv(os.Environ()), "EVAL_OUTPUT="+outPath, "EVAL_DIR="+ws, "EVAL_HIDDEN_GATE="+gate)
	return cmd.Run() == nil
}

func lockedFiles(t eval.Task) map[string]string {
	m := map[string]string{}
	for _, n := range t.Locked {
		m[n] = t.Files[n]
	}
	return m
}
