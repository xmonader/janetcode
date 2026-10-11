// Command squatscreen is the false-alarm half of the typosquat
// tripwire's pre-screen (internal/typosquat; the detection half is
// TestSeededTyposquats): over recorded session logs, with no model call,
// it runs the check on every import and requirement a model's edit
// added, and prints each one it flags. The workspaces the logs ran in
// are gone, so each name is compared with the well-known lists alone, as
// in an empty project: the strictest the check gets. Any flag on a
// recorded edit is a false alarm unless the model really did misspell a
// package.
//
//	squatscreen docs/benchmarks/effbench/runs ~/.local/share/janetcode/sessions
//
// An edit_file call is judged by its old and new text, a write_file call
// by its content against nothing.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/typosquat"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "squatscreen:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("give one or more directories of session logs")
	}
	known := typosquat.Popular()
	var edits, withAdded, names, flagged int
	seen, distinct := map[string]bool{}, map[string]bool{}
	for _, dir := range args {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
				return err
			}
			return eachEdit(p, func(path, before, after string) {
				edits++
				added := typosquat.Added(path, []byte(before), []byte(after))
				if len(added) > 0 {
					withAdded++
				}
				for _, n := range added {
					names++
					distinct[n.Eco+" "+n.Name] = true
					if f, ok := typosquat.Check(known, n); ok {
						flagged++
						key := f.Eco + " " + f.Name
						if !seen[key] {
							seen[key] = true
							fmt.Printf("flagged: %s %s %s %s\n", f.Eco, f.Name, f.Kind, f.Like)
						}
					}
				}
			})
		})
		if err != nil {
			return err
		}
	}
	rate := 0.0
	if names > 0 {
		rate = 100 * float64(flagged) / float64(names)
	}
	ecos := map[string]int{}
	for k := range distinct {
		ecos[strings.Fields(k)[0]]++
	}
	fmt.Printf("edits %d, edits adding a dependency name %d, names added %d (%d distinct: %v), flagged %d (%.1f%%)\n", edits, withAdded, names, len(distinct), ecos, flagged, rate)
	return nil
}

// eachEdit calls f for every edit_file and write_file call in a log.
func eachEdit(path string, f func(file, before, after string)) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var e session.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type != session.TypeToolCall {
			continue
		}
		var in struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Old     string `json:"old_string"`
			New     string `json:"new_string"`
		}
		if json.Unmarshal([]byte(e.Arguments), &in) != nil || !typosquat.Source(in.Path) {
			continue
		}
		switch e.Name {
		case "write_file":
			f(in.Path, "", in.Content)
		case "edit_file":
			f(in.Path, in.Old, in.New)
		}
	}
	return sc.Err()
}
