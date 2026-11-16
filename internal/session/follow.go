package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"
)

// TypeFollowReset is the event Follow delivers when the file was rewritten
// under it: it is not in any session file. What follows is the whole log
// again, so an observer clears what it showed and starts over.
const TypeFollowReset = "follow/reset"

// Follow is the read-only way to watch a session another process runs:
// the session file is an append-only event log, so anything that can read
// it can follow along without taking the lease or touching the owner's
// state. It calls fn for every event after sequence number after (0 for
// the whole log), then for each event as it is appended, polling the file
// every interval. It returns ctx's error when ctx ends, or the first error
// fn or the file gives; a file that does not exist yet is waited for.
//
// While the file only grows, each event is delivered once and in order
// (what was delivered is skipped by sequence number, and a line still
// being written waits for its newline). A rewind or a compaction rewrites
// the file, and a crash can tear its last line; Follow notices that the
// file was replaced or shrank, announces it with a TypeFollowReset event,
// and delivers the whole log again, because a rewind can make the log
// reuse sequence numbers.
func Follow(ctx context.Context, path string, after int, interval time.Duration, fn func(Event) error) error {
	if interval <= 0 {
		interval = 200 * time.Millisecond
	}
	last := after
	var (
		f      *os.File
		off    int64
		carry  []byte
		sizeAt int64
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()
	for {
		if f == nil {
			var err error
			if f, err = os.Open(path); err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					return err
				}
			} else {
				off, carry, sizeAt = 0, nil, 0
			}
		}
		if f != nil {
			st, err := f.Stat()
			if err != nil {
				return err
			}
			if cur, serr := os.Stat(path); serr != nil || !os.SameFile(st, cur) || cur.Size() < off {
				// Replaced or cut: read the new file from the top.
				f.Close()
				f = nil
				if serr != nil && !errors.Is(serr, os.ErrNotExist) {
					return serr
				}
				last = 0
				if err := fn(Event{Type: TypeFollowReset, Time: time.Now()}); err != nil {
					return err
				}
				if err := sleep(ctx, interval); err != nil {
					return err
				}
				continue
			}
			if st.Size() > sizeAt || len(carry) > 0 {
				n, err := readNew(f, off, &carry, &last, fn)
				off += n
				if err != nil {
					return err
				}
				sizeAt = st.Size()
			}
		}
		if err := sleep(ctx, interval); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// readNew reads what was appended after off, hands every complete event
// line past *last to fn, and keeps a trailing partial line in carry. It
// returns how many bytes it consumed.
func readNew(f *os.File, off int64, carry *[]byte, last *int, fn func(Event) error) (int64, error) {
	buf, err := io.ReadAll(io.NewSectionReader(f, off, 1<<62))
	if err != nil {
		return 0, err
	}
	data := append(*carry, buf...)
	var consumed int
	for {
		i := bytes.IndexByte(data[consumed:], '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(data[consumed : consumed+i])
		consumed += i + 1
		if len(line) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(line, &r) != nil || r.Type != "event" || r.Seq <= *last {
			continue
		}
		*last = r.Seq
		if err := fn(r.Event); err != nil {
			return 0, err
		}
	}
	*carry = append([]byte(nil), data[consumed:]...)
	return int64(len(buf)), nil
}
