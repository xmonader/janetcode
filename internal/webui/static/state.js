// The page's shared view model: the one object the areas of the web UI read
// and write instead of closing over each other's variables.
//
// STATE MAP (who writes, who reads). Areas are files; "app.js" is what is
// left of the old monolith (transcript render, event stream, composer,
// sessions rail, workspace pane, palette, approvals/questions, guest bar).
//
//   connected   writes: app.js applyHello (the hello event)
//               reads:  app.js model chip/composer send/palette, switchmenu.js
//   lastWorkspace writes: app.js refreshWorkspace
//               reads:  app.js turn file cards, workspace pane, modemenu.js
//   firstUserText writes: app.js event stream (user entries, reset)
//               reads:  app.js header title, sessionsrail.js (live row label)
//   localNote   writes: app.js status, sessionsrail.js (resume failed)
//               reads:  app.js finishStatus
//   latest      writes: app.js render/event stream (set, delete, clear on reset)
//               reads:  app.js (tool groups, find, markdown export, copy/edit
//               buttons), workspacepane.js (has the user spoken yet)
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
    // every transcript entry by id, newest version of each.
    latest: new Map(),
    // the conversation's first prompt: it names the conversation until the
    // server titles it.
    firstUserText: "",
    // a message the page made itself (not the server's status); the status
    // line keeps showing it until the next server status replaces it.
    localNote: "",
    // the last /api/workspace payload (files, git, switches), or null.
    lastWorkspace: null,
    // false on a first run with no provider key.
    connected: true,
  };
})(window);
