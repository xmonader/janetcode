package webui

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// TestWorkspaceCarriesTheDryRunPlan: while the session is a dry run the
// workspace payload holds the same plan the headless result object
// carries (mode, sandbox, files, calls not made, hosts, the estimate and
// its basis); outside one it holds none.
func TestWorkspaceCarriesTheDryRunPlan(t *testing.T) {
	ts, ag := testApp(t)
	defer ts.Close()
	fetch := func() workspace {
		t.Helper()
		res, err := http.Get(ts.URL + "/api/workspace")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var ws workspace
		if err := json.NewDecoder(res.Body).Decode(&ws); err != nil {
			t.Fatal(err)
		}
		return ws
	}
	if ws := fetch(); ws.DryRun || ws.DryPlan != nil {
		t.Fatalf("a normal session shows a plan: %+v", ws.DryPlan)
	}
	ag.SetDryRun(true)
	ws := fetch()
	if !ws.DryRun || ws.DryPlan == nil {
		t.Fatalf("dry run without a plan: %+v", ws)
	}
	want := ag.DryRunPlan()
	if !reflect.DeepEqual(*ws.DryPlan, want) {
		t.Fatalf("web plan differs from the agent's:\n got %+v\nwant %+v", *ws.DryPlan, want)
	}
	if ws.DryPlan.Mode == "" || ws.DryPlan.Sandbox == "" || ws.DryPlan.Estimate.Basis == "" || ws.DryPlan.Files == nil || ws.DryPlan.Actions == nil {
		t.Fatalf("plan is missing parts: %+v", ws.DryPlan)
	}
}
