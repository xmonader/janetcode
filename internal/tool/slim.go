package tool

// Slim tool schemas are what a headless run advertises. Every request
// carries the tool list, and janetcode's was 6.5 KB against pi's 3 KB:
// about 900 tokens on each of ~20 calls a run. Over 3,267 Kimi tool calls
// on effbench (realrepo and synthetic, 168 sessions) the fields the slim
// schemas leave out were used: then_run 0 times, bash background 2,
// stdin 1, interactive 0, read_file anchors 1, line and anchor edits 24,
// a lone old_string outside edits 26, tool_search 3. The tools still
// accept every field; only the advertisement shrinks.
// JANETCODE_OFF=slimtools restores the full schemas.
