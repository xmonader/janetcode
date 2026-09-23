package trajectory

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// routeEvidenceEvents is a two-step turn whose second model call left the
// session's own model: a cheap-step route, the shape the fold must keep
// visible (github issue #4).
func routeEvidenceEvents() []session.Event {
	return []session.Event{
		{Seq: 1, Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Seq: 2, Type: session.TypeAssistant, Turn: 1, Step: 1, Surface: true,
			Message: msg(llm.RoleAssistant, "reading the file"),
			Slot:    "main", Provider: "anthropic", Model: "main-model"},
		{Seq: 3, Type: session.TypeStepStart, Turn: 1, Step: 2},
		{Seq: 4, Type: session.TypeAssistant, Turn: 1, Step: 2, Surface: true,
			Message: msg(llm.RoleAssistant, "it says alpha"),
			Slot:    "cheap", Provider: "openrouter", Model: "cheap-model",
			Route: "route", RouteWhy: "the previous step was a clean, mechanical batch"},
	}
}

func TestFoldKeepsRouteEvidenceOnTheModelRecord(t *testing.T) {
	l := Fold(routeEvidenceEvents())
	if len(l.All) != 2 {
		t.Fatalf("records = %d", len(l.All))
	}
	main, routed := l.All[0], l.All[1]
	if main.Slot != "main" || main.Route != "" || main.RouteWhy != "" {
		t.Fatalf("main call must report no route: %+v", main)
	}
	if routed.Slot != "cheap" || routed.Route != "route" || routed.RouteWhy == "" {
		t.Fatalf("routed call lost its evidence: %+v", routed)
	}
}

func TestLineMarksARoutedModelCall(t *testing.T) {
	l := Fold(routeEvidenceEvents())
	if line := Line(l.All[0], 100); strings.Contains(line, "↪") {
		t.Fatalf("a main call must not wear a route marker: %q", line)
	}
	line := Line(l.All[1], 100)
	if !strings.Contains(line, "↪ cheap") {
		t.Fatalf("the routed call must say which slot answered: %q", line)
	}
}

func TestInspectExplainsTheRoute(t *testing.T) {
	l := Fold(routeEvidenceEvents())
	out := Inspect(l, 2)
	if !strings.Contains(out, "route    ↪ cheap") || !strings.Contains(out, "mechanical") {
		t.Fatalf("the inspector must name the slot and the reason:\n%s", out)
	}
	if out := Inspect(l, 1); strings.Contains(out, "route    ") {
		t.Fatalf("a main call has no route to explain:\n%s", out)
	}
}

func TestSearchFindsRecordsByRouteEvidence(t *testing.T) {
	l := Fold(routeEvidenceEvents())
	if hits := Search(l, "mechanical"); len(hits) != 1 || hits[0].Index != 2 {
		t.Fatalf("search by route reason = %+v", hits)
	}
	if hits := Search(l, "cheap"); len(hits) != 1 || hits[0].Index != 2 {
		t.Fatalf("search by slot = %+v", hits)
	}
}
