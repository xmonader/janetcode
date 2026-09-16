package session

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/fsutil"
)

// Rating is a thumbs judgment of one assistant message.
type Rating string

const (
	RateUp   Rating = "up"
	RateDown Rating = "down"
)

// Feedback is one sidecar rating, keyed by assistant/message seq.
type Feedback struct {
	Seq    int       `json:"seq"`
	Rating Rating    `json:"rating"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
}

func (s *Session) feedbackPath() string {
	return strings.TrimSuffix(s.path, ".jsonl") + ".feedback.json"
}

// LoadFeedback reads the sidecar; missing file is empty.
func (s *Session) LoadFeedback() ([]Feedback, error) {
	data, err := os.ReadFile(s.feedbackPath())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Feedback
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SaveFeedback writes the sidecar. It is rewritten whole on every
// rating, so it goes through the same temp-file-and-rename the log does:
// a crash halfway must not cost the ratings already given.
func (s *Session) SaveFeedback(items []Feedback) error {
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWriteMode(s.feedbackPath(), append(data, '\n'), fsutil.PrivateFileMode)
}

// Rate last assistant/message (or seq if >0).
func (s *Session) Rate(seq int, rating Rating, note string) error {
	if seq == 0 {
		// The log is appended to from the turn's tool goroutines while
		// the UI rates a message, so take a snapshot rather than reading
		// the live slice.
		ev := s.Events()
		for i := len(ev) - 1; i >= 0; i-- {
			if ev[i].Type == TypeAssistant {
				seq = ev[i].Seq
				break
			}
		}
	}
	if seq == 0 {
		return os.ErrInvalid
	}
	items, err := s.LoadFeedback()
	if err != nil {
		return err
	}
	fb := Feedback{Seq: seq, Rating: rating, Note: strings.TrimSpace(note), At: time.Now().UTC()}
	replaced := false
	for i, it := range items {
		if it.Seq == seq {
			items[i] = fb
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, fb)
	}
	return s.SaveFeedback(items)
}
