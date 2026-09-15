package agent

import (
	"reflect"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// lit fills in the two settings a struct literal cannot: CompactAt and
// ContextWindow are promoted from the embedded contextManager.
func lit(a *Agent, window, compactAt int) *Agent {
	a.ContextWindow, a.CompactAt = window, compactAt
	return a
}

// TestNewMatchesLiteral builds the same agent by struct literal and by New
// and compares what a front-end sees of it, so New's defaults cannot drift
// from the literal's.
func TestNewMatchesLiteral(t *testing.T) {
	prov := &fake{}
	tools := tool.NewRegistry()
	log := &session.Memory{}
	cases := []struct {
		name    string
		s       Settings
		literal *Agent
	}{
		{"zero", Settings{}, &Agent{Provider: prov, Tools: tools, Log: log}},
		{"session", Settings{Model: "m", Price: &llm.Price{Input: 3, Output: 15}, ContextWindow: 200000, MaxTokens: 8000, MaxIterations: 100, CompactAt: 150000, AutoEdits: true, ProofNotes: true},
			lit(&Agent{Provider: prov, Tools: tools, Log: log, Model: "m", Price: &llm.Price{Input: 3, Output: 15}, MaxTokens: 8000, MaxIterations: 100, AutoEdits: true, ProofNotes: true}, 200000, 150000)},
		{"replay", Settings{AutoApprove: true, ReadOnly: true, MaxIterations: 200},
			&Agent{Provider: prov, Tools: tools, Log: log, AutoApprove: true, ReadOnly: true, MaxIterations: 200}},
		{"probe", Settings{Model: "m", MaxTokens: 1000, MaxIterations: 6, AutoApprove: true, CompactAt: -1},
			lit(&Agent{Provider: prov, Tools: tools, Log: log, Model: "m", MaxTokens: 1000, MaxIterations: 6, AutoApprove: true}, 0, -1)},
		{"fork", Settings{AutoMode: true, PlanMode: true},
			&Agent{Provider: prov, Tools: tools, Log: log, AutoMode: true, PlanMode: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, err := New(Deps{Provider: prov, Tools: tools, Log: log}, c.s)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := a.Settings(), c.literal.Settings(); !reflect.DeepEqual(got, want) {
				t.Errorf("New settings = %+v\nliteral settings = %+v", got, want)
			}
			if a.Tools != tools || a.Log != log {
				t.Error("New dropped a dependency")
			}
		})
	}
}

// TestNewCopiesEverySetting fails when a field is added to Settings
// without New carrying it onto the Agent.
func TestNewCopiesEverySetting(t *testing.T) {
	var s Settings
	v := reflect.ValueOf(&s).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("x")
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Pointer:
			f.Set(reflect.New(f.Type().Elem()))
		case reflect.Interface:
			// Provider: comes from Deps.
		default:
			t.Fatalf("Settings.%s has kind %s, which this test does not fill", v.Type().Field(i).Name, f.Kind())
		}
	}
	prov := &fake{}
	a, err := New(Deps{Provider: prov, Tools: tool.NewRegistry()}, s)
	if err != nil {
		t.Fatal(err)
	}
	s.Provider = prov
	if got := a.Settings(); !reflect.DeepEqual(got, s) {
		t.Errorf("New settings = %+v, want %+v", got, s)
	}
}

func TestNewRefusesMissingDeps(t *testing.T) {
	prov, tools := &fake{}, tool.NewRegistry()
	for name, c := range map[string]struct {
		d Deps
		s Settings
	}{
		"no tools":             {Deps{Provider: prov}, Settings{}},
		"provider in settings": {Deps{Provider: prov, Tools: tools}, Settings{Provider: prov}},
	} {
		if _, err := New(c.d, c.s); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
