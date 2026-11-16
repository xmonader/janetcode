// The header's switch menu: the model picker (fed by /api/models) and the
// reasoning-effort picker share one menu. Reads S.connected (a model cannot
// be picked before a provider is connected; the click opens /connect
// instead); everything else is private to the menu. app.js passes the
// helpers it shares and gets the two openers and the closer back.
(function (global) {
  "use strict";
  global.JanetSwitchMenu = function ({ S, esc, post, openConnect }) {
    // The header's switch menu. The model button opens a searchable picker
    // fed by /api/models; the effort chip opens the reasoning tiers. One
    // menu serves both, the way the palette serves every command.
    const modelBtn = document.getElementById("model");
    const effortChip = document.getElementById("effort-chip");
    const switchMenu = document.getElementById("switch-menu");
    const switchQ = document.getElementById("switch-q");
    const switchList = document.getElementById("switch-list");
    const stopBtn = document.getElementById("stop");
    const EFFORT_TIERS = ["low", "medium", "high", "xhigh", "max"];
    let switchMode = "model";
    let modelGroups = [];
    let switchRows = [];
    let switchIdx = 0;
    let switchAnchor = null;
    let switchGen = 0;

    function closeSwitchMenu(restore = true) {
      if (switchMenu.hidden) return;
      switchGen++;
      switchMenu.hidden = true;
      modelBtn.setAttribute("aria-expanded", "false");
      effortChip.setAttribute("aria-expanded", "false");
      switchQ.setAttribute("aria-expanded", "false");
      switchQ.removeAttribute("aria-activedescendant");
      const anchor = switchAnchor;
      switchAnchor = null;
      if (restore && anchor) anchor.focus();
    }
    function showSwitchMenu(anchor) {
      closeSwitchMenu(false);
      switchAnchor = anchor;
      modelBtn.setAttribute("aria-expanded", anchor === modelBtn ? "true" : "false");
      effortChip.setAttribute("aria-expanded", anchor === effortChip ? "true" : "false");
      switchQ.setAttribute("aria-expanded", "true");
      switchMenu.hidden = false;
      switchQ.focus();
    }
    function placeSwitchMenu(anchor) {
      const r = anchor.getBoundingClientRect();
      const width = Math.min(320, window.innerWidth - 16);
      const left = Math.max(8, Math.min(r.right - width, window.innerWidth - width - 8));
      const below = window.innerHeight - r.bottom;
      const above = r.top;
      const up = below < 220 && above > below;
      switchMenu.classList.toggle("up", up);
      switchMenu.style.left = left + "px";
      switchMenu.style.width = width + "px";
      if (up) {
        switchMenu.style.top = "auto";
        switchMenu.style.bottom = (window.innerHeight - r.top + 6) + "px";
        switchMenu.style.maxHeight = Math.max(160, above - 12) + "px";
      } else {
        switchMenu.style.bottom = "auto";
        switchMenu.style.top = (r.bottom + 6) + "px";
        switchMenu.style.maxHeight = Math.max(160, below - 12) + "px";
      }
    }

    async function openModelMenu() {
      switchMode = "model";
      switchQ.value = "";
      switchQ.placeholder = "Search model or provider…";
      switchQ.setAttribute("aria-label", "Search models and providers");
      switchList.innerHTML = `<div class="menu-empty" role="status">Loading…</div>`;
      placeSwitchMenu(modelBtn);
      showSwitchMenu(modelBtn);
      const gen = ++switchGen;
      let groups = [];
      try {
        groups = (await (await fetch("/api/models")).json()).groups || [];
      } catch (_) {}
      if (gen !== switchGen || switchMenu.hidden || switchMode !== "model") return;
      modelGroups = groups;
      renderSwitchMenu(switchQ.value);
    }

    function openEffortMenu() {
      switchMode = "effort";
      switchQ.value = "";
      switchQ.placeholder = "Set effort…";
      switchQ.setAttribute("aria-label", "Search reasoning effort");
      placeSwitchMenu(effortChip);
      showSwitchMenu(effortChip);
      renderSwitchMenu("");
    }

    // contextSize is a window as people say it: "128k context", "1M context".
    function contextSize(n) {
      if (!n) return "";
      if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M context";
      return Math.round(n / 1000) + "k context";
    }

    function rowHTML(i, main, sub) {
      return `<div id="switch-option-${i}" class="menu-row" role="option" aria-selected="false" data-i="${i}"><span>${esc(main)}</span>` + (sub ? `<span class="menu-sub">${esc(sub)}</span>` : "") + `</div>`;
    }

    function paintSwitchRows() {
      let active = null;
      switchList.querySelectorAll(".menu-row").forEach((el) => {
        const on = Number(el.dataset.i) === switchIdx;
        el.classList.toggle("on", on);
        el.setAttribute("aria-selected", on ? "true" : "false");
        if (on) active = el;
      });
      if (active) {
        switchQ.setAttribute("aria-activedescendant", active.id);
        active.scrollIntoView({ block: "nearest" });
      } else switchQ.removeAttribute("aria-activedescendant");
    }

    function renderSwitchMenu(q) {
      q = q.trim().toLowerCase();
      switchRows = [];
      let html = "";
      if (switchMode === "model") {
        for (const g of modelGroups) {
          // The model in use leads its group, so the menu opens on it
          // rather than on whatever sorts first ("aion-labs/…").
          const words = q.split(/\s+/).filter(Boolean);
          const rows = g.models.filter((m) => words.every((word) =>
            [g.provider, g.label, m.model].filter(Boolean).join(" ").toLowerCase().includes(word)))
            .sort((a, b) => (b.current ? 1 : 0) - (a.current ? 1 : 0));
          if (!rows.length) continue;
          html += `<div class="menu-group" role="presentation">${esc(g.label || g.provider)} · ${rows.length}</div>`;
          for (const m of rows) {
            switchRows.push({ cmd: "/model " + g.provider + " " + m.model, current: m.current });
            const provider = g.label && g.label !== g.provider ? `${g.label} (${g.provider})` : g.provider;
            html += rowHTML(switchRows.length - 1, m.model + (m.current ? " ✓" : ""),
              [provider, contextSize(m.context)].filter(Boolean).join(" · "));
          }
        }
        // A name nobody listed is still switchable: /model routes it by
        // family or prefix, and remembers it.
        if (q && !switchRows.length && !modelGroups.some((g) => g.models.some((m) => m.model.toLowerCase() === q))) {
          switchRows.push({ cmd: "/model " + q });
          html = rowHTML(0, `switch to “${q}”`, "not listed; use it by name") + html;
        }
      } else {
        const cur = effortChip.dataset.effort || "";
        for (const t of EFFORT_TIERS) {
          if (q && !t.includes(q)) continue;
          switchRows.push({ cmd: "/effort " + t });
          html += rowHTML(switchRows.length - 1, t + (cur === t ? " ✓" : ""), "");
        }
      }
      switchList.innerHTML = html || `<div class="menu-empty">Nothing matches</div>`;
      const current = switchRows.findIndex((r) => r.current);
      switchIdx = current >= 0 ? current : 0;
      paintSwitchRows();
    }

    function chooseSwitch(i) {
      const r = switchRows[i];
      if (!r) return;
      closeSwitchMenu();
      post("/api/command", { text: r.cmd });
    }

    modelBtn.addEventListener("click", (ev) => {
      ev.stopPropagation();
      if (!S.connected) { openConnect(); return; }
      if (switchMenu.hidden || switchMode !== "model") openModelMenu();
      else closeSwitchMenu();
    });
    effortChip.addEventListener("click", (ev) => {
      ev.stopPropagation();
      if (switchMenu.hidden || switchMode !== "effort") openEffortMenu();
      else closeSwitchMenu();
    });
    switchQ.addEventListener("input", () => renderSwitchMenu(switchQ.value));
    switchQ.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") { ev.preventDefault(); closeSwitchMenu(); return; }
      if (ev.key === "Tab") { closeSwitchMenu(); return; }
      if (!switchRows.length) return;
      if (ev.key === "ArrowDown") { ev.preventDefault(); switchIdx = (switchIdx + 1) % switchRows.length; paintSwitchRows(); return; }
      if (ev.key === "ArrowUp") { ev.preventDefault(); switchIdx = (switchIdx - 1 + switchRows.length) % switchRows.length; paintSwitchRows(); return; }
      if (ev.key === "Home") { ev.preventDefault(); switchIdx = 0; paintSwitchRows(); return; }
      if (ev.key === "End") { ev.preventDefault(); switchIdx = switchRows.length - 1; paintSwitchRows(); return; }
      if (ev.key === "Enter") { ev.preventDefault(); chooseSwitch(switchIdx); }
    });
    switchList.addEventListener("click", (ev) => {
      const row = ev.target.closest("[data-i]");
      if (row) chooseSwitch(Number(row.dataset.i));
    });
    document.addEventListener("click", (ev) => {
      if (switchMenu.hidden) return;
      if (!ev.target.closest("#switch-menu") && !ev.target.closest("#model") && !ev.target.closest("#effort-chip")) closeSwitchMenu(false);
    });
    stopBtn.addEventListener("click", async () => {
      await post("/api/cancel", {});
    });
    return { openModelMenu, openEffortMenu, closeSwitchMenu, el: switchMenu };
  };
})(window);
