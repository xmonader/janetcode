package critic

import (
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// recorded is a ledger of the shape a real session leaves behind: the
// work interleaved with the harness's own interventions.
func recorded() []session.Event {
	at := time.Date(2026, 9, 11, 14, 30, 0, 0, time.UTC)
	t := func(n int) time.Time { return at.Add(time.Duration(n) * time.Second) }
	return []session.Event{
		{Seq: 1, Time: t(0), Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Time: t(1), Type: session.TypeUserMessage, Turn: 1, Surface: true,
			Message: &llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "fix the parser"}}}},
		{Seq: 3, Time: t(2), Type: session.TypeToolCall, Turn: 1, Name: "read_file", CallID: "c1"},
		{Seq: 4, Time: t(3), Type: session.TypeToolResult, Turn: 1, Name: "read_file", CallID: "c1", Output: "package main"},
		{Seq: 5, Time: t(4), Type: session.TypeToolResult, Turn: 1, Name: "bash", CallID: "c2", IsError: true,
			Output: "The user denied permission for this tool call. Ask before retrying or choose another approach."},
		{Seq: 6, Time: t(5), Type: session.TypeNotice, Turn: 1,
			Output: "429 rate limited · falling back to claude-haiku-5"},
		{Seq: 7, Time: t(6), Type: session.TypeToolResult, Turn: 1, Name: "bash", CallID: "c3", IsError: true,
			Output: "exit status 2: undefined: foo"},
		{Seq: 8, Time: t(7), Type: session.TypeCompactStart, Turn: 2,
			Output: "context pruned: ~90000 → ~40000 tokens"},
		{Seq: 9, Time: t(8), Type: session.TypeNotice, Turn: 2,
			Output: "1 of 2 verification commands failed · sending the model back to fix it (1/3)"},
		// A verdict that agrees with the model is not an intervention;
		// one that contradicts it is.
		{Seq: 10, Time: t(9), Type: session.TypeVerdict, Turn: 2, Name: "no changes", Output: "nothing here edited code"},
		{Seq: 10, Time: t(9), Type: session.TypeVerdict, Turn: 2, Name: "unverified", IsError: true,
			Output: "code changed and nothing was run"},
		{Seq: 11, Time: t(10), Type: session.TypeNotice, Turn: 3,
			Output: "turn stopped: the same tool call three times with no new result"},
		{Seq: 12, Time: t(11), Type: session.TypeTurnEnd, Turn: 3, Reason: "stuck"},
	}
}

func TestInterventionsPickOutTheHarnessAndNothingElse(t *testing.T) {
	ivs := Interventions(recorded())
	var got []InterventionKind
	for _, iv := range ivs {
		got = append(got, iv.Kind)
	}
	want := []InterventionKind{IvRefused, IvFallback, IvCompaction, IvVerify, IvVerdict, IvLoop}
	if len(got) != len(want) {
		t.Fatalf("got %v, wanted %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d: got %q, wanted %q", i, got[i], want[i])
		}
	}
}

// Every turn ends with a verdict, so counting the ones that agree with
// the model made /why answer "verdict · nothing here edited code" after
// a session in which the harness had done nothing at all, and buried
// the compaction or the refusal that /why exists for.
func TestAVerdictThatAgreesIsNotAnIntervention(t *testing.T) {
	for _, iv := range Interventions(recorded()) {
		if strings.Contains(iv.Text, "nothing here edited code") {
			t.Fatal("an agreeable verdict was reported as an intervention")
		}
	}
	got := WhyLast([]session.Event{
		{Seq: 1, Type: session.TypeVerdict, Turn: 1, Name: "verified", Output: "tests pass"},
	}, 1)
	if !strings.Contains(got, "has not intervened") {
		t.Fatalf("a session of nothing but agreeable verdicts claimed an intervention:\n%s", got)
	}
}

// Every wording a refusal actually reaches the ledger in. --deny
// produces the third of these, and it used to be invisible to /why.
func TestEveryShapeOfRefusalIsSeenAsOne(t *testing.T) {
	for _, out := range []string{
		// agent.DeniedMessage, spelled out: this package must not
		// import the one that defines it. The agent-side test
		// TestHarnessRefusalsAreVisibleToWhy checks the constant
		// itself is still one of these shapes.
		"The user denied permission for this tool call. Ask before retrying or choose another approach.",
		"Permission denied: no approver configured for mutating tools.",
		"Denied by permission rule read_file. Do not retry this call; choose another approach or ask the user to change the rule.",
		"refused: read-only mode is on",
	} {
		ivs := Interventions([]session.Event{
			{Seq: 1, Type: session.TypeToolResult, Turn: 1, Name: "read_file", IsError: true, Output: out},
		})
		if len(ivs) != 1 || ivs[0].Kind != IvRefused {
			t.Errorf("%q was not read as a refusal: %v", textfmt.FirstLine(out, 70), ivs)
		}
	}
}

// The "↻ retried 2× · rate limited · waited 1.5s" line in the transcript
// used to be the only record that the harness had waited and bought a
// call twice, so /why answered with whatever had happened before it.
func TestARetryIsSomethingTheHarnessDid(t *testing.T) {
	got := WhyLast([]session.Event{
		{Seq: 1, Type: session.TypeNotice, Turn: 1, Output: "retried 2× · rate limited · waited 1.5s"},
	}, 1)
	if !strings.HasPrefix(got, string(IvRetry)) {
		t.Fatalf("a retry was not explained as one:\n%s", got)
	}
	if !strings.Contains(got, "rate limit") {
		t.Fatalf("no explanation:\n%s", got)
	}
}

func TestAToolThatSimplyFailedIsNotAnIntervention(t *testing.T) {
	for _, iv := range Interventions(recorded()) {
		if strings.Contains(iv.Text, "undefined: foo") {
			t.Fatal("a compiler error was reported as something the harness did")
		}
	}
}

func TestWhyExplainsTheMostRecentInterventionInPlainWords(t *testing.T) {
	got := WhyLast(recorded(), 1)
	if !strings.HasPrefix(got, "loop ladder (turn 3") {
		t.Fatalf("headline is wrong:\n%s", got)
	}
	if !strings.Contains(got, "the same tool call three times") {
		t.Fatalf("the harness's own words are missing:\n%s", got)
	}
	if !strings.Contains(got, "repeating itself") {
		t.Fatalf("no explanation:\n%s", got)
	}
	if !strings.Contains(got, "/why 2") {
		t.Fatalf("does not say how to go further back:\n%s", got)
	}
}

func TestWhyNCountsBackwards(t *testing.T) {
	for n, want := range map[int]InterventionKind{
		1: IvLoop, 2: IvVerdict, 3: IvVerify, 4: IvCompaction, 5: IvFallback, 6: IvRefused,
	} {
		got := WhyLast(recorded(), n)
		if !strings.HasPrefix(got, string(want)) {
			t.Errorf("/why %d began %q, wanted %s", n, textfmt.FirstLine(got, 70), want)
		}
	}
}

func TestWhyBeyondTheLedgerSaysSoRatherThanFailing(t *testing.T) {
	got := WhyLast(recorded(), 99)
	if !strings.Contains(got, "6 harness interventions") {
		t.Fatalf("%q", got)
	}
}

func TestWhyOnAQuietSessionSaysNothingHappened(t *testing.T) {
	quiet := []session.Event{{Seq: 1, Type: session.TypeTurnStart}, {Seq: 2, Type: session.TypeToolCall, Name: "read_file"}}
	got := WhyLast(quiet, 1)
	if !strings.Contains(got, "has not intervened") {
		t.Fatalf("%q", got)
	}
}

func TestEveryKindHasAnExplanation(t *testing.T) {
	for _, k := range []InterventionKind{
		IvCompaction, IvLoop, IvVerify, IvVerdict, IvFallback, IvWatchdog, IvBudget,
		IvRefused, IvRecovery, IvArchive, IvIntegrity, IvHook, IvPermission,
		IvSteering, IvWorkingMem, IvPathRule, IvPlan, IvNudge, IvOversized,
	} {
		if because[k] == "" {
			t.Errorf("%s has no explanation", k)
		}
	}
}

func TestNoticesAreClassifiedByWhatTheySay(t *testing.T) {
	for _, tc := range []struct {
		text string
		want InterventionKind
	}{
		{"turn stopped: a cycle of three calls", IvLoop},
		{"verifying: go test ./...", IvVerify},
		{"quota exhausted · falling back to gpt-6-mini", IvFallback},
		{"stream stalled after 240s", IvWatchdog},
		{"the model wrote read_file as text instead of calling it; recovered and run", IvRecovery},
		{"archive fault (1/3): the exit code of the build", IvArchive},
		{"request integrity: an unanswered tool call was repaired", IvIntegrity},
		{"stop hook sent the model back to work (1/3): not done", IvHook},
		{"saved permission rule: bash(go test *)", IvPermission},
		{"steering the turn: use the other parser", IvSteering},
		{"working memory: the model returned prose instead of the record; kept as notes", IvWorkingMem},
		{"something nobody has a pattern for", IvNudge},
	} {
		if got := classifyNotice(tc.text); got != tc.want {
			t.Errorf("%q → %s, wanted %s", tc.text, got, tc.want)
		}
	}
}
