// The page's shared view model: the one object the areas of the web UI read
// and write instead of closing over each other's variables.
//
// STATE MAP (who writes, who reads). Areas are files; "app.js" is what is
// left of the old monolith (transcript render, event stream, composer,
// sessions rail, workspace pane, palette, approvals/questions, guest bar).
//
//   lastReply   writes: app.js transcript render (assistant text)
//               reads:  notify.js (the body of the "turn finished" popup)
//
// Fields move here only when a second file needs them; a field that one
// area owns stays a local of that area. The remaining module-level state of
// app.js, and why it is still local there, is listed in PROJECT-STATE.md.
(function (global) {
  "use strict";
  global.JanetState = {
    lastReply: "",
  };
})(window);
