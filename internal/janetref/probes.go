package janetref

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/gitx"
)

// The probe memory: discriminating probes (internal/agent/probes.go) —
// inline checks that failed on the code before a change and pass after
// it — outlive the session that ran them, in the repository, under
// ProbesRef. A later session replays them at its gate, so a fix made last
// week is held by the check that showed it worked, without the model
// having to remember the lesson or be told it in prose. A probe is a
// command, not text the model reads as an instruction.

// ProbesRef is the probe memory's ref.
const ProbesRef = Prefix + "probes"

// probesFile is the record file holding the probes.
const probesFile = "probes.json"

// MaxProbes bounds the memory; past it the oldest probes go first.
const MaxProbes = 64

// probesVersion is the schema version of probes.json.
const probesVersion = 1

// StoredProbe is one remembered probe.
type StoredProbe struct {
	// Cmd is the command with the workspace path taken out (see
	// Relativize), so it runs in another clone of the repository.
	Cmd string `json:"cmd"`
	// Why is the stated intent of the change the probe told apart from
	// the code it replaced, for the note a failing replay carries.
	Why     string    `json:"why,omitempty"`
	Session string    `json:"session,omitempty"`
	Added   time.Time `json:"added"`
}

type probeRecord struct {
	Version int           `json:"version"`
	Probes  []StoredProbe `json:"probes"`
}

// LoadProbes is the probe memory and the commit it was read from ("" when
// there is none yet).
func LoadProbes(ctx context.Context, repo string) ([]StoredProbe, string, error) {
	tip, err := Tip(ctx, repo, ProbesRef)
	if err != nil || tip == "" {
		return nil, "", err
	}
	data, ok, err := ReadAt(ctx, repo, tip, probesFile)
	if err != nil || !ok {
		return nil, tip, err
	}
	var rec probeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, tip, fmt.Errorf("%s: %w", ProbesRef, err)
	}
	return rec.Probes, tip, nil
}

// AddProbes adds probes the memory does not hold yet (by Cmd) and reports
// how many were new. The oldest go past MaxProbes.
func AddProbes(ctx context.Context, repo string, add []StoredProbe) (int, error) {
	n := 0
	err := updateProbes(ctx, repo, func(cur []StoredProbe) ([]StoredProbe, string) {
		n = 0
		have := map[string]bool{}
		for _, p := range cur {
			have[p.Cmd] = true
		}
		for _, p := range add {
			if p.Cmd == "" || have[p.Cmd] {
				continue
			}
			have[p.Cmd] = true
			cur = append(cur, p)
			n++
		}
		if len(cur) > MaxProbes {
			cur = cur[len(cur)-MaxProbes:]
		}
		return cur, fmt.Sprintf("probes: remember %d", n)
	})
	return n, err
}

// DropProbes removes the probes with these commands and reports how many
// were there.
func DropProbes(ctx context.Context, repo string, cmds []string) (int, error) {
	n := 0
	err := updateProbes(ctx, repo, func(cur []StoredProbe) ([]StoredProbe, string) {
		n = 0
		drop := map[string]bool{}
		for _, c := range cmds {
			drop[c] = true
		}
		var keep []StoredProbe
		for _, p := range cur {
			if drop[p.Cmd] {
				n++
				continue
			}
			keep = append(keep, p)
		}
		return keep, fmt.Sprintf("probes: retire %d", n)
	})
	return n, err
}

// updateProbes applies edit to the memory and writes it back, retrying a
// few times when another session wrote in between. An edit that changes
// nothing writes nothing.
func updateProbes(ctx context.Context, repo string, edit func([]StoredProbe) ([]StoredProbe, string)) error {
	for range 4 {
		cur, tip, err := LoadProbes(ctx, repo)
		if err != nil {
			return err
		}
		before, _ := json.Marshal(cur)
		next, msg := edit(append([]StoredProbe(nil), cur...))
		after, _ := json.Marshal(next)
		if string(before) == string(after) {
			return nil
		}
		data, err := json.MarshalIndent(probeRecord{Version: probesVersion, Probes: next}, "", "  ")
		if err != nil {
			return err
		}
		_, err = Write(ctx, repo, ProbesRef, tip, msg, map[string][]byte{probesFile: append(data, '\n')})
		if errors.Is(err, ErrConflict) {
			continue
		}
		return err
	}
	return ErrConflict
}

// FetchProbes fetches remote's probe memory and merges it into this one:
// probes are unioned rather than one side's ledger replacing the other's.
func FetchProbes(ctx context.Context, repo, remote string) (int, error) {
	const theirs = Prefix + "fetched/probes"
	if err := Fetch(ctx, repo, remote, "+"+ProbesRef, theirs); err != nil {
		return 0, err
	}
	defer gitx.Run(ctx, repo, nil, "update-ref", "-d", theirs)
	tip, err := Tip(ctx, repo, theirs)
	if err != nil || tip == "" {
		return 0, err
	}
	data, ok, err := ReadAt(ctx, repo, tip, probesFile)
	if err != nil || !ok {
		return 0, err
	}
	var rec probeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return 0, fmt.Errorf("remote %s: %w", ProbesRef, err)
	}
	return AddProbes(ctx, repo, rec.Probes)
}

// Relativize takes the workspace path out of a probe command: a mention
// of root/x becomes ./x and root itself ".", since a replay runs with the
// workspace (or a copy of it) as its working directory.
func Relativize(cmd, root string) string {
	root = strings.TrimRight(root, "/")
	if root == "" || root == "." {
		return cmd
	}
	var b strings.Builder
	for {
		i := strings.Index(cmd, root)
		if i < 0 {
			b.WriteString(cmd)
			return b.String()
		}
		rest := cmd[i+len(root):]
		switch {
		case strings.HasPrefix(rest, "/"):
			// root/x: the slash stays, as ./x.
			b.WriteString(cmd[:i] + ".")
		case rest == "" || !pathByte(rest[0]):
			b.WriteString(cmd[:i] + ".")
		default:
			// root is only the start of a longer name: /work in /workspace.
			b.WriteString(cmd[:i+len(root)])
		}
		cmd = rest
	}
}

// pathByte reports whether c can continue a path component.
func pathByte(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
