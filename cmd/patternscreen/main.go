// patternscreen runs the pattern-induction idea's offline pre-screen
// (internal/editpattern's Screen) over recorded effbench campaigns: of the
// runs, how many made three or more structurally similar hunks. It reads;
// it never calls a model.
//
//	patternscreen docs/benchmarks/effbench/runs/jc-mimo-r*
//	patternscreen -arm pi docs/benchmarks/effbench/runs/jc-mimo-r*
//
// Every run directory under a campaign's arm is read, whatever its grade:
// the question is about the shape of the edits, not whether they passed.
// janetcode runs are read from janetcode-sessions/ (edit_file calls); pi
// runs from pi-session/ (its edit tool's edits list), so the screen can
// say whether the answer depends on the harness.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xmonader/janetcode/internal/editpattern"
	"github.com/xmonader/janetcode/internal/session"
)

func main() {
	arm := flag.String("arm", "janetcode", "the arm whose runs to read (janetcode or pi)")
	flag.Parse()
	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: patternscreen [-arm janetcode|pi] <campaign dir>...")
		os.Exit(2)
	}
	var runs []editpattern.Run
	for _, dir := range flag.Args() {
		dirs, _ := filepath.Glob(filepath.Join(dir, *arm, "*-s*"))
		sort.Strings(dirs)
		for _, d := range dirs {
			var edits []editpattern.Edit
			var err error
			if *arm == "pi" {
				edits, err = piEdits(filepath.Join(d, "pi-session"))
			} else {
				edits, err = janetcodeEdits(filepath.Join(d, "janetcode-sessions"))
			}
			if err != nil {
				continue
			}
			runs = append(runs, editpattern.Run{Name: filepath.Base(dir) + "/" + filepath.Base(d), Edits: edits})
		}
	}
	fmt.Print(editpattern.Screen(runs).Text())
}

// janetcodeEdits reads every session a headless run left behind.
func janetcodeEdits(dir string) ([]editpattern.Edit, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no session in %s", dir)
	}
	sort.Strings(matches)
	var all []editpattern.Edit
	for _, m := range matches {
		s, _, err := session.Open(dir, strings.TrimSuffix(filepath.Base(m), ".jsonl"))
		if err != nil {
			return nil, err
		}
		all = append(all, editpattern.EditsFromEvents(s.Events())...)
	}
	return all, nil
}

// piEdits reads pi's session log: assistant messages whose content holds
// toolCall parts, the edit tool's arguments being a path and a list of
// {oldText, newText}.
func piEdits(dir string) ([]editpattern.Edit, error) {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if len(matches) == 0 {
		return nil, fmt.Errorf("no session in %s", dir)
	}
	sort.Strings(matches)
	var out []editpattern.Edit
	for _, m := range matches {
		f, err := os.Open(m)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			if !strings.Contains(sc.Text(), `"toolCall"`) {
				continue
			}
			var line struct {
				Message struct {
					Content []struct {
						Type      string `json:"type"`
						Name      string `json:"name"`
						Arguments struct {
							Path  string `json:"path"`
							Edits []struct {
								Old string `json:"oldText"`
								New string `json:"newText"`
							} `json:"edits"`
						} `json:"arguments"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &line) != nil {
				continue
			}
			for _, c := range line.Message.Content {
				if c.Type != "toolCall" || c.Name != "edit" {
					continue
				}
				for _, e := range c.Arguments.Edits {
					if e.Old != "" {
						out = append(out, editpattern.Edit{Path: c.Arguments.Path, Old: e.Old, New: e.New})
					}
				}
			}
		}
		f.Close()
	}
	return out, nil
}
