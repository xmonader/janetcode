package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Limits on messages to subagents. A model that loops on this tool must
// not be able to bury a child (or the budget) under notes it never reads.
const (
	// maxMessageBytes bounds one message.
	maxMessageBytes = 16 * 1024
	// maxPendingMessages bounds the messages waiting for one child: notes
	// not yet delivered to its next model call plus those queued as later
	// turns. A child that is not reading them is not helped by more.
	maxPendingMessages = 8
	// maxMessagesPerChild bounds every message ever sent to one child.
	maxMessagesPerChild = 32
)

// Delivery is how a message reaches a subagent that is running. A
// subagent that is not running is woken and runs the text as its next
// turn whatever the delivery.
type Delivery string

const (
	// DeliverSteer joins the current turn before its next model call.
	DeliverSteer Delivery = "steer"
	// DeliverQueue waits and runs as a later turn.
	DeliverQueue Delivery = "queue"
	// DeliverInterject is a steer that goes ahead of steering already
	// waiting.
	DeliverInterject Delivery = "interject"
)

// ParseDelivery reads a delivery name; empty is steer.
func ParseDelivery(s string) (Delivery, error) {
	switch d := Delivery(strings.ToLower(strings.TrimSpace(s))); d {
	case "":
		return DeliverSteer, nil
	case DeliverSteer, DeliverQueue, DeliverInterject:
		return d, nil
	}
	return "", fmt.Errorf("delivery %q: want steer, queue or interject", s)
}

// resolveChild finds a subagent by id ("task-3") or, failing that, by its
// description, which is the name the user sees in /agents. A description
// must match one child; it is compared whole, ignoring case, then as a
// prefix. The lock must be held.
func (t *Task) resolveChild(ref string) (string, *subagent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil, fmt.Errorf("subagent_id is empty")
	}
	if kid := t.children[ref]; kid != nil {
		return ref, kid, nil
	}
	for _, match := range []func(desc string) bool{
		func(d string) bool { return strings.EqualFold(d, ref) },
		func(d string) bool { return len(d) >= len(ref) && strings.EqualFold(d[:len(ref)], ref) },
	} {
		var ids []string
		for _, id := range slices.Sorted(mapKeys(t.children)) {
			if match(strings.TrimSpace(t.children[id].desc)) {
				ids = append(ids, id)
			}
		}
		switch len(ids) {
		case 0:
			continue
		case 1:
			return ids[0], t.children[ids[0]], nil
		}
		return "", nil, fmt.Errorf("%q names several subagents (%s); use an id", ref, strings.Join(ids, ", "))
	}
	return "", nil, fmt.Errorf("no subagent %q started by this session (ids: %s)", ref, strings.Join(slices.Sorted(mapKeys(t.children)), ", "))
}

func mapKeys[V any](m map[string]V) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Message sends text to a subagent this session started, by id or
// description. It returns what happened in a line for the model.
//
//   - A running subagent gets the text by delivery: steer (before its next
//     model call), interject (the same, ahead of steering already waiting)
//     or queue (a later turn, once the current one ends).
//   - A subagent that has finished is woken in the background and runs the
//     text as its next turn; its answer comes back like a fork's. An
//     editing subagent is not woken this way (its worktree and patch are
//     the task tool's: use continue).
//
// Past the limits above nothing is sent.
func (t *Task) Message(ctx context.Context, ref, text string, d Delivery) (string, error) {
	if t == nil || t.Parent == nil {
		return "", fmt.Errorf("this session has no subagents")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text is empty")
	}
	if n := len(text); n > maxMessageBytes {
		return "", fmt.Errorf("message is %d bytes; the limit is %d", n, maxMessageBytes)
	}
	if d == "" {
		d = DeliverSteer
	}
	t.mu.Lock()
	id, kid, err := t.resolveChild(ref)
	if err != nil {
		t.mu.Unlock()
		return "", err
	}
	if kid.agent == nil {
		t.mu.Unlock()
		return "", fmt.Errorf("subagent %s has no agent", id)
	}
	if kid.sent >= maxMessagesPerChild {
		t.mu.Unlock()
		return "", fmt.Errorf("message quota reached: %d messages already sent to %s; start a new subagent or use continue", kid.sent, id)
	}
	running := !kid.done
	if running && kid.agent.pendingSteer()+len(kid.queued) >= maxPendingMessages {
		t.mu.Unlock()
		return "", fmt.Errorf("%s already has %d messages waiting; it has not read them yet", id, maxPendingMessages)
	}
	if !running && kid.wt != nil {
		t.mu.Unlock()
		return "", fmt.Errorf("%s edits in a worktree and has finished; resume it with task continue=%s", id, id)
	}
	kid.sent++
	msgID := fmt.Sprintf("msg-%s-%d", id, kid.sent)
	note := "Message from the parent agent (" + msgID + "):\n" + text
	if running {
		switch d {
		case DeliverQueue:
			kid.queued = append(kid.queued, note)
			t.mu.Unlock()
			return fmt.Sprintf("queued %s for %s: it runs as a later turn after the current one ends (task continue=%s also runs it)", msgID, id, id), nil
		case DeliverInterject:
			kid.agent.Interject(note)
		default:
			kid.agent.Steer(note)
		}
		t.mu.Unlock()
		return fmt.Sprintf("delivered %s to %s (%s): it reads it before its next model call", msgID, id, d), nil
	}
	// Finished: wake it.
	kid.done = false
	kid.last = "woken by a message"
	kid.started = time.Now()
	desc, start := kid.desc, kid.started
	label := "subagent"
	if kid.fork {
		label = "fork"
	}
	t.mu.Unlock()
	t.inheritBudget(kid.agent)
	emit := func(ev Event) {
		t.mu.Lock()
		defer t.mu.Unlock()
		switch ev.Kind {
		case EvToolRun:
			if ev.Call != nil {
				kid.last = ev.Call.Name + " " + briefInput(string(ev.Call.Input))
			}
		case EvPhase:
			kid.last = ev.Text
		}
	}
	t.runBackground(kid, id, subagentKind(kid, nil), desc, note, 0, label, start, emit)
	return fmt.Sprintf("%s was not running; woke it with %s as its next turn. Its answer arrives as a background update", id, msgID), nil
}

// SendSubagentMessage is the model-callable side of Task.Message. It stays
// on the parent session: a subagent cannot message another, so messaging
// is one level deep by construction, and the check in Run holds even for a
// fork, which inherits the parent's tools.
type SendSubagentMessage struct {
	Task *Task
}

func (*SendSubagentMessage) Name() string { return "send_subagent_message" }
func (*SendSubagentMessage) Description() string {
	return "Send a message to a subagent you started (by its id, or its description), while it works or after it finished. delivery steer (default): it reads the text before its next model call. interject: the same, ahead of anything waiting. queue: it runs the text as a later turn once the current one ends. A finished subagent is woken and runs the text as its next turn. Limited: 16 KB per message, 8 waiting per subagent, 32 in all."
}
func (*SendSubagentMessage) Mutating() bool   { return false }
func (*SendSubagentMessage) ParentOnly() bool { return true }
func (*SendSubagentMessage) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"subagent_id":{"type":"string","description":"The subagent's id (task-3) or its description"},
"text":{"type":"string","description":"What to tell it"},
"delivery":{"type":"string","enum":["steer","queue","interject"],"description":"steer (default), queue or interject"}},
"required":["subagent_id","text"],"additionalProperties":false}`)
}

func (s *SendSubagentMessage) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		ID       string `json:"subagent_id"`
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if s.Task == nil || s.Task.Parent == nil {
		return "", fmt.Errorf("this session has no subagents")
	}
	if s.Task.Parent.Forked {
		return "", fmt.Errorf("a fork cannot message subagents: only the main session can")
	}
	d, err := ParseDelivery(in.Delivery)
	if err != nil {
		return "", err
	}
	return s.Task.Message(ctx, in.ID, in.Text, d)
}
