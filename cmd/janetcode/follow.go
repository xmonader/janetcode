package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/headless"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/sessionproto"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// followMode is --follow: watch a session another janetcode runs, read
// only. It never takes the session's lease, so the process that owns the
// session (a terminal, a --web server, an editor over --acp, a --bg run)
// is not disturbed and cannot tell it is watched. It reads the session
// file the owner appends to, and the --bg status file beside it for what
// a background run is waiting on.
//
// The first lines say who holds the session. It ends when the holder is
// gone: finished, or killed, since the kernel drops the lease with the
// process, with a line saying so. A session nobody holds is printed and
// the command ends, because nothing more will be written to it.
//
// With --output-format stream-json each line is one JSON object of the
// session protocol (docs/manual/protocol.md): {"protocol":1,"kind":...}
// where kind is lease, event, bg or end.
func followMode(ctx context.Context, w io.Writer, dir, id string, format headless.Format, interval time.Duration) error {
	path, err := session.BgSessionPath(dir, id)
	if err != nil {
		return headless.BadInput("%v", err)
	}
	if _, err := os.Stat(path); err != nil {
		return headless.BadInput("no session %s (janetcode --sessions lists them)", id)
	}
	out := &followOut{w: w, json: format == headless.FormatStreamJSON}

	holder, held := session.Holder(path)
	out.lease(id, holder, held)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	followErr := make(chan error, 1)
	go func() { followErr <- session.Follow(ctx, path, 0, interval, out.event) }()

	// The watch ends once the owner has been gone for two looks in a row
	// and the log has been read to its end after that.
	reason := "interrupted"
	var lastBg session.BgStatus
	tick := time.NewTicker(interval)
	defer tick.Stop()
	gone := 0
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err := <-followErr:
			if err != nil && err != context.Canceled {
				return err
			}
			break loop
		case <-tick.C:
			if st, ok, _ := session.ReadBgStatus(path); ok && (st.State != lastBg.State || st.Detail != lastBg.Detail || bgAskID(st) != bgAskID(lastBg)) {
				lastBg = st
				out.bg(st)
			}
			if _, now := session.Holder(path); now {
				gone = 0
				continue
			}
			gone++
			if !held {
				reason = "idle"
				break loop
			}
			if gone >= 2 {
				reason = "owner_gone"
				break loop
			}
		}
	}
	// Let the follower deliver what the owner wrote last.
	if reason != "interrupted" {
		time.Sleep(2 * interval)
	}
	cancel()
	<-followErr
	out.end(reason)
	return nil
}

func bgAskID(st session.BgStatus) string {
	if st.Ask != nil {
		return st.Ask.ID
	}
	return ""
}

type followOut struct {
	mu   sync.Mutex
	w    io.Writer
	json bool
}

func (o *followOut) obj(kind string, fields map[string]any) {
	fields["protocol"] = sessionproto.Version
	fields["kind"] = kind
	b, _ := json.Marshal(fields)
	fmt.Fprintf(o.w, "%s\n", b)
}

func (o *followOut) lease(id string, in session.LeaseInfo, held bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		f := map[string]any{"session": id, "held": held, "role": sessionproto.RoleObserver}
		if held {
			f["holder"] = in
		}
		o.obj("lease", f)
		return
	}
	if held {
		fmt.Fprintf(o.w, "following %s read-only; %s has it\n", id, describeHolder(in))
		return
	}
	fmt.Fprintf(o.w, "%s is not held by any janetcode; its log so far:\n", id)
}

func describeHolder(in session.LeaseInfo) string {
	who := in.Holder
	if who == "" {
		who = "a janetcode"
	}
	return fmt.Sprintf("%s (pid %d)", who, in.PID)
}

func (o *followOut) event(e session.Event) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		o.obj("event", map[string]any{"event": e})
		return nil
	}
	if line := eventLine(e); line != "" {
		fmt.Fprintln(o.w, line)
	}
	return nil
}

func (o *followOut) bg(st session.BgStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		o.obj("bg", map[string]any{"status": st})
		return
	}
	fmt.Fprintf(o.w, "-- background run: %s\n", st.Line(time.Now()))
}

func (o *followOut) end(reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.json {
		o.obj("end", map[string]any{"reason": reason})
		return
	}
	switch reason {
	case "owner_gone":
		fmt.Fprintln(o.w, "-- the session's owner is gone (finished or killed); the lease is free")
	case "idle":
		fmt.Fprintln(o.w, "-- nothing is running this session")
	}
}

// eventLine is one event as a line of text, or "" for the bookkeeping
// events a person following along does not need.
func eventLine(e session.Event) string {
	switch e.Type {
	case session.TypeFollowReset:
		return "-- the session file was rewritten (rewind or compaction); from the top again"
	case session.TypeTurnStart:
		return fmt.Sprintf("-- turn %d", e.Turn)
	case session.TypeUserMessage:
		if e.Message != nil {
			return "you: " + textfmt.Clip(oneLine(e.Message.Text()), 300)
		}
	case session.TypeAssistant:
		if e.Message != nil {
			if t := oneLine(e.Message.Text()); t != "" {
				return "assistant: " + textfmt.Clip(t, 300)
			}
		}
	case session.TypeToolCall:
		return "tool " + e.Name + " " + textfmt.Clip(oneLine(e.Arguments), 120)
	case session.TypeToolResult:
		tag := "  result"
		if e.Denied {
			tag = "  denied"
		} else if e.IsError {
			tag = "  failed"
		}
		return tag + " " + textfmt.Clip(oneLine(e.Output), 160)
	case session.TypeNotice:
		return "notice: " + textfmt.Clip(oneLine(strings.TrimSpace(e.Name+" "+e.Output)), 200)
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
