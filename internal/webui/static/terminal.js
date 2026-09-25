// The terminal tab: xterm.js over a websocket bridged to a pty in the
// session's directory. Built once, kept across view switches; the shell
// dies with the socket, so leaving the view and coming back starts clean
// only if the socket actually dropped.
(function (global) {
  "use strict";

  // JanetTerminal draws into termView and returns what app.js needs: show
  // and hide for the view ring, restyle for a theme change.
  global.JanetTerminal = function (termView) {
    let term = null;
    let termFit = null;
    let termWS = null;
    function termTheme() {
      const dark = document.documentElement.dataset.theme === "dark";
      return dark ? {
        background: "#161618",
        foreground: "rgba(255,255,255,0.9)",
        cursor: "#0a84ff",
        selectionBackground: "#2f3b52",
      } : {
        background: "#ffffff",
        foreground: "#1d1d1f",
        cursor: "#0071e3",
        selectionBackground: "#cfe3fb",
      };
    }
    function fitTerm() {
      if (!termFit || termView.clientHeight < 40) return;
      termFit.fit();
      if (termWS && termWS.readyState === WebSocket.OPEN) {
        termWS.send(JSON.stringify({ type: "resize", rows: term.rows, cols: term.cols }));
      }
    }
    function ensureTerminal() {
      if (term && termWS && termWS.readyState === WebSocket.OPEN) {
        term.options.theme = termTheme();
        fitTerm();
        term.focus();
        return;
      }
      if (term) {
        term.dispose();
        term = null;
        termFit = null;
      }
      // xterm measures an actual family. A CSS variable name makes the cell
      // width disagree with the glyphs, so a prompt and an ls land on one row.
      term = new Terminal({
        fontFamily: '"JetBrains Mono", ui-monospace, monospace',
        fontSize: 14,
        lineHeight: 1.2,
        cursorBlink: true,
        theme: termTheme(),
      });
      termFit = new FitAddon.FitAddon();
      term.loadAddon(termFit);
      term.open(termView);
      const proto = location.protocol === "https:" ? "wss" : "ws";
      termWS = new WebSocket(`${proto}://${location.host}/api/term`);
      termWS.onmessage = (ev) => {
        try {
          const m = JSON.parse(ev.data);
          if (m.type === "out") term.write(m.data);
        } catch (_) {}
      };
      termWS.onopen = () => {
        term.onData((d) => termWS.readyState === WebSocket.OPEN && termWS.send(JSON.stringify({ type: "in", data: d })));
        document.fonts.ready.then(fitTerm);
        fitTerm();
        new ResizeObserver(() => fitTerm()).observe(termView);
        term.focus();
      };
      // A shell whose server went away used to sit on its last prompt,
      // taking keystrokes that went nowhere.
      termWS.onclose = () => {
        if (term) term.write("\r\n\x1b[2m[disconnected: the shell ended. Open the Terminal tab again for a new one]\x1b[0m\r\n");
      };
    }

    return {
      show() {
        termView.hidden = false;
        ensureTerminal();
        requestAnimationFrame(() => fitTerm());
      },
      hide() { termView.hidden = true; },
      restyle() { if (term) term.options.theme = termTheme(); },
    };
  };
})(window);
