package tool

// NotReplayable names tools whose effect is only on the running
// conversation, or that themselves dispatch another chain of tool calls:
// replaying one outside a model turn either does nothing useful or means
// something different. A subagent (task, workflow) has no one to report
// back to; a nested run_code or execute_code has no accounting for its own
// nesting; ask has no one to ask; cron, todo and goal_status manage state
// that belongs to a live conversation, not a fixed script; expand_output
// only ever reveals something the conversation already stashed.
//
// internal/agent's run_code and internal/recipe (which cannot import
// agent, where these tools are defined, without a cycle) both read this
// map when deciding what a fixed list of steps may contain; extend it
// here, not in a second list.
var NotReplayable = map[string]bool{
	"task": true, "workflow": true, "execute_code": true, "run_code": true,
	"ask": true, "cron": true, "todo": true, "goal_status": true, "expand_output": true,
}
