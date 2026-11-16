// The composer's permission-mode chip and its menu. Reads S.lastWorkspace
// (the last /api/workspace payload: which of yolo/auto/edits/plan/readonly/
// away are on) to mark the active row; picking a row sends the server's
// toggle commands through post. The palette's "Permission mode" action and
// app.js's Escape handler use the returned paint/el/hide.
(function (global) {
  "use strict";
  global.JanetModeMenu = function ({ S, post }) {
    // The composer mode chip is a switcher, not a label: it opens the
    // permission menu right here, with the same toggle commands as the
    // session-panel switches. Ask first is the only row that is not a
    // server toggle — it turns off whatever is on.
    const MODE_ITEMS = [
      { key: "ask",       label: "Ask first",       what: "every tool asks first",              cmd: null },
      { key: "AUTO",      label: "Auto",            what: "edits and safe reads run, commands ask", cmd: "/auto" },
      { key: "EDITS",     label: "Auto-edit",       what: "file changes go through, commands still ask", cmd: "/edits" },
      { key: "PLAN",      label: "Plan first",      what: "read-only until the plan is approved", cmd: "/plan" },
      { key: "READ-ONLY", label: "Read-only",       what: "inspect, change nothing",            cmd: "/readonly" },
      { key: "YOLO",      label: "Allow all tools", what: "every tool runs without asking",     cmd: "/yolo" },
      { key: "AWAY",      label: "Away",            what: "tools run, questions get an assumption; turning it off lists what happened", cmd: "/afk" },
    ];
    const modeMenuEl = document.getElementById("mode-menu");
    const modeChip = document.getElementById("mode-chip");
    function hideModeMenu(restore = true) {
      if (modeMenuEl.hidden) return;
      modeMenuEl.hidden = true;
      modeChip.setAttribute("aria-expanded", "false");
      if (restore) modeChip.focus();
    }
    function activeModeKeys() {
      const ws = S.lastWorkspace || {};
      // Away rides on top of the auto-approve switch: it is the one mode shown.
      if (ws.away) return ["AWAY"];
      return [ws.yolo && "YOLO", ws.auto && "AUTO", ws.edits && "EDITS", ws.plan && "PLAN", ws.readonly && "READ-ONLY"].filter(Boolean);
    }
    function paintModeMenu() {
      const on = activeModeKeys();
      modeMenuEl.innerHTML = MODE_ITEMS.map((it) => {
        const isOn = it.key === "ask" ? !on.length : on.includes(it.key);
        return `<button type="button" role="menuitemradio" aria-checked="${isOn}" tabindex="-1" class="${isOn ? "on" : ""}" data-key="${it.key}" data-testid="mode-opt-${it.key.toLowerCase()}"><span class="mk" aria-hidden="true">✓</span><span class="name">${it.label}</span><span>${it.what}</span></button>`;
      }).join("");
      const selected = modeMenuEl.querySelector(".on") || modeMenuEl.querySelector("button");
      if (selected) selected.tabIndex = 0;
    }
    function openModeMenu() {
      paintModeMenu();
      modeMenuEl.hidden = false;
      modeChip.setAttribute("aria-expanded", "true");
      const selected = modeMenuEl.querySelector(".on") || modeMenuEl.querySelector("button");
      if (selected) selected.focus();
    }
    modeChip.addEventListener("click", () => {
      if (!modeMenuEl.hidden) hideModeMenu(); else openModeMenu();
    });
    modeChip.addEventListener("keydown", (ev) => {
      if ((ev.key === "ArrowDown" || ev.key === "ArrowUp") && modeMenuEl.hidden) { ev.preventDefault(); openModeMenu(); }
      if (ev.key === "Escape" && !modeMenuEl.hidden) { ev.preventDefault(); hideModeMenu(); }
    });
    modeMenuEl.addEventListener("keydown", (ev) => {
      const rows = [...modeMenuEl.querySelectorAll("button")];
      const at = rows.indexOf(document.activeElement);
      let next = -1;
      if (ev.key === "ArrowDown") next = (at + 1) % rows.length;
      else if (ev.key === "ArrowUp") next = (at - 1 + rows.length) % rows.length;
      else if (ev.key === "Home") next = 0;
      else if (ev.key === "End") next = rows.length - 1;
      else if (ev.key === "Escape") { ev.preventDefault(); hideModeMenu(); return; }
      else if (ev.key === "Tab") { hideModeMenu(); return; }
      else return;
      ev.preventDefault();
      if (rows[next]) rows[next].focus();
    });
    modeMenuEl.addEventListener("click", async (ev) => {
      const row = ev.target.closest("[data-key]");
      if (!row) return;
      hideModeMenu();
      // The rows pick one mode; the server commands toggle. So switch off
      // every other mode that is on, and send the chosen one only when it
      // is not on already, or picking the current mode would turn it off.
      const item = MODE_ITEMS.find((it) => it.key === row.dataset.key);
      const active = activeModeKeys();
      // Leaving Away is /afk alone, which puts the earlier settings back;
      // picking Away from another mode is /afk alone too.
      if (active.includes("AWAY") || item.key === "AWAY") { await post("/api/command", { text: "/afk" }); return; }
      const cmds = active.filter((k) => k !== item.key).map((k) => MODE_ITEMS.find((it) => it.key === k).cmd);
      if (item.cmd && !active.includes(item.key)) cmds.push(item.cmd);
      for (const c of cmds) await post("/api/command", { text: c });
    });
    document.addEventListener("click", (ev) => {
      if (!modeMenuEl.hidden && !ev.target.closest("#mode-menu") && !ev.target.closest("#mode-chip")) hideModeMenu(false);
    });
    return { paint: paintModeMenu, hide: hideModeMenu, el: modeMenuEl };
  };
})(window);
