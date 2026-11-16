// The page's shared view model: the one object the areas of the web UI read
// and write instead of closing over each other's variables.
//
// AREAS (files, load order in index.html; each is JanetX({S, ...deps})):
//   app.js           transcript render, event stream (handle/EventSource),
//                    header/status/model chips, mermaid, turn file cards
//   find.js          Ctrl+F in the thread        settings.js  theme, dialog, ding
//   panels.js        rails, preview drawer, drag-resize
//   notify.js        desktop notifications       switchmenu.js model/effort menu
//   modemenu.js      permission-mode menu        sessionsrail.js sessions rail
//   workspacepane.js Changes/Jobs/Agents/Plan    approvals.js ask/questions/secret
//   composer.js      send, history, @-complete, drafts, slash menu
//   palette.js       Ctrl+K palette              guest.js     guest links
//
// STATE MAP, fields of S (W = writes, R = reads):
//   connected     W app.js applyHello          R app.js, composer.js, switchmenu.js
//   running       W app.js applyStatus         R app.js (send/stop buttons), composer.js
//   pageSession   W app.js applyHello          R app.js (reset, JanetApp), composer.js (draft key)
//   commands      W app.js (/api/commands)     R composer.js (slash menu), palette.js
//   lastPrompt    W app.js reset, composer.js  R app.js (retry buttons)
//   pending       W approvals.js showAsk       R app.js, composer.js (y/a/r/n/c)
//   latest        W app.js render/reset        R app.js, workspacepane.js, approvals.js
//   firstUserText W app.js stream/reset        R app.js (title), sessionsrail.js
//   localNote     W app.js, sessionsrail.js    R app.js applyStatus
//   lastWorkspace W workspacepane.js           R app.js, modemenu.js, workspacepane.js
//   lastReply     W app.js render              R notify.js
//
// STILL LOCAL TO app.js (one area owns them, or they are render caches):
//   nodes/open* sets/lastCreated  transcript DOM caches, written by render()
//   curTitle lastUserText lastFlavor suggestedFor   header + empty-page
//   modelLabel modelProvider unseen lastServerStatus   header/status chips
//   linkPrompt* (?prompt= link)  pinned (scroll tail)  currentView  dragDepth  lost
// Rule: a field moves into S only when a second file needs it.
(function (global) {
  "use strict";
  global.JanetState = {
    lastReply: "",
    // a turn is running (from the status event).
    running: false,
    // the session id this page is showing; keys the unsent draft.
    pageSession: "",
    // the server's slash commands (/api/commands).
    commands: [],
    // the last prompt sent that was not a slash command (retry resends it).
    lastPrompt: "",
    // the permission request on the approval card, or null.
    pending: null,
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
