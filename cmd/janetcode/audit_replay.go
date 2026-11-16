package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/xmonader/janetcode/internal/auditreplay"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/whatif"
)

// errAuditReplayDiverged is --audit-replay's exit when the recording does not
// reproduce, so a script can gate on it.
var errAuditReplayDiverged = errors.New("audit-replay: the recorded run does not reproduce from its log")

// loadAudited reads a session and checks its audit chain.
func loadAudited(arg string) (*session.Session, session.AuditReport, error) {
	path, dir, id, err := whatif.ResolveSession(arg)
	if err != nil {
		return nil, session.AuditReport{}, err
	}
	rep, err := session.VerifyAudit(path)
	if err != nil {
		return nil, rep, err
	}
	s, _, err := session.Open(dir, id)
	if err != nil {
		return nil, rep, err
	}
	return s, rep, nil
}

// auditReplayMode is --audit-replay: rebuild every model request of a
// recorded session from its log alone (system prompt, tool schemas,
// messages), join each tool call to its authorization, result and file
// changes, and compare the rebuild with the digests recorded when the
// requests were sent. It opens no provider, runs no tool and touches
// neither the network nor the workspace; it needs no credentials.
func auditReplayMode(w io.Writer, arg string) error {
	s, audit, err := loadAudited(arg)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "session %s  %s\n", s.Meta.ID, s.Label())
	fmt.Fprintf(w, "directory %s · %s/%s\n\n", s.Meta.Cwd, s.Meta.Provider, s.Meta.Model)
	rep := auditreplay.Replay(s.Events())
	fmt.Fprint(w, rep.Text())
	rerun := rerunDivergence(s.Path())
	if rerun == "" {
		fmt.Fprintln(w, "re-run: the recorded replies drive the agent loop to the same tool calls with the same arguments")
	} else {
		fmt.Fprintf(w, "re-run: DIVERGED, %s\n", rerun)
	}
	fmt.Fprint(w, auditSummary(audit))
	if !rep.OK() || !audit.OK() || rerun != "" {
		return errAuditReplayDiverged
	}
	return nil
}

// rerunDivergence feeds the recording's model replies back through the
// agent loop (the --replay machinery: read-only tools on an empty root,
// every other tool a stub, a provider that serves the recording and
// nothing else) and reports the first difference between the tool calls
// it makes now and the ones the log holds, arguments included. Empty
// means the same calls in the same order.
func rerunDivergence(path string) string {
	recorded, now, err := replaySession(context.Background(), path, io.Discard)
	if err != nil {
		return err.Error()
	}
	want, got := callSignatures(recorded), callSignatures(now)
	for i := 0; i < max(len(want), len(got)); i++ {
		switch {
		case i >= len(want):
			return fmt.Sprintf("call %d, %s, was not recorded", i+1, got[i])
		case i >= len(got):
			return fmt.Sprintf("call %d, %s, was recorded but the re-run never made it", i+1, want[i])
		case want[i] != got[i]:
			return fmt.Sprintf("call %d: recorded %s, re-run %s", i+1, want[i], got[i])
		}
	}
	return ""
}

// callSignatures is each tool call the model asked for itself, as its
// name and its arguments compacted to one line.
func callSignatures(events []session.Event) []string {
	var out []string
	for _, e := range events {
		if e.Type != session.TypeToolCall || session.IsNestedCall(e.CallID) {
			continue
		}
		args := e.Arguments
		var buf bytes.Buffer
		if json.Compact(&buf, []byte(args)) == nil {
			args = buf.String()
		}
		out = append(out, e.Name+" "+textfmt.Clip(args, 200))
	}
	return out
}

// registerConfiguredSecrets makes every credential this process could
// leak a registered secret, so an export removes the values themselves,
// whatever shape they have: the configured providers' keys and every
// environment variable named like a credential.
func registerConfiguredSecrets() {
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if ok && secret.CredentialName(name) {
			secret.Register(val)
		}
	}
	cfg, err := config.Load(config.Overrides{})
	if err != nil || cfg == nil {
		return
	}
	for _, p := range cfg.Providers {
		for _, k := range p.Keys() {
			secret.Register(k)
		}
	}
}

// auditExportMode is --audit-export: write the portable audit bundle of a
// session to w. No API key or configured secret value appears in it.
func auditExportMode(w io.Writer, arg string) error {
	s, audit, err := loadAudited(arg)
	if err != nil {
		return err
	}
	registerConfiguredSecrets()
	b, err := auditreplay.Export(s.Meta, audit, s.Events())
	if err != nil {
		return err
	}
	data, err := b.Marshal()
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}
