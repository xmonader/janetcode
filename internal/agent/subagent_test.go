package agent

import "testing"

func TestAgentsArg(t *testing.T) {
	in, start, err := AgentsArg("fork do the review")
	if err != nil || !start || !in.Fork || in.Prompt != "do the review" || in.Agent != "" || in.Kind() != "fork" {
		t.Fatalf("fork = %#v %v %v", in, start, err)
	}
	in, start, err = AgentsArg("new explore the tree\nand report")
	if err != nil || !start || in.Fork || in.Agent != "general" || in.Description != "explore the tree" || in.Kind() != "subagent" {
		t.Fatalf("new = %#v %v %v", in, start, err)
	}
	for _, arg := range []string{"", "a1b2", "newish thing"} {
		if _, start, err := AgentsArg(arg); start || err != nil {
			t.Fatalf("AgentsArg(%q) should name a child, not start one", arg)
		}
	}
	for _, arg := range []string{"new", "fork", "  fork  "} {
		if _, _, err := AgentsArg(arg); err == nil {
			t.Fatalf("AgentsArg(%q) wants a brief", arg)
		}
	}
}

func TestReviewTasks(t *testing.T) {
	ins := ReviewTasks("", "DIFF")
	if len(ins) != 2 || ins[0].Agent != "explore" || ins[1].Agent != "general" {
		t.Fatalf("review = %#v", ins)
	}
}
