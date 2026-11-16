// The command palette (Ctrl+K): the list, fuzzy filter, keyboard handling
// and running an item. What the page can do is app.js's to say: it passes
// appActions(act), which returns the action rows; this file adds extras.js's
// actions and the server's slash commands (S.commands). Returns open/close
// (the /connect dialog closes it) and the element (other Esc handlers ask
// whether it is open).
(function (global) {
  "use strict";
  global.JanetPalette = function ({ S, esc, post, input, resize, appActions }) {
    const palette = document.getElementById("palette");
    const paletteQ = document.getElementById("palette-q");
    const paletteList = document.getElementById("palette-list");
    let palIdx = 0;
    let palItems = [];

    function openPalette() {
      palette.hidden = false;
      paletteQ.value = "";
      palIdx = 0;
      drawPalette("");
      paletteQ.focus();
    }
    function closePalette() { palette.hidden = true; input.focus(); }

    // The palette merges everything the page can do: app.js's actions,
    // the extras.js ones and the server's slash commands. Each item
    // carries its own run(); a slash command that takes arguments fills the
    // composer instead of running.
    function paletteActions() {
      const act = (group, name, help, run, key) => ({ group, name, help, run, key });
      const mdFor = (c) => act("Commands", c.name, c.help || "", async () => {
        if (c.args) { input.value = c.name + " "; resize(); input.focus(); } else await post("/api/command", { text: c.name });
      });
      return appActions(act).concat(window.JanetExtras && window.JanetExtras.actions ? window.JanetExtras.actions(act) : [], S.commands.map(mdFor));
    }
    function drawPalette(q) {
      q = q.toLowerCase().replace(/^\//, "");
      const fuzzy = (n) => {
        let i = 0;
        for (const ch of n) if (ch === q[i] && ++i === q.length) return true;
        return false;
      };
      palItems = paletteActions().filter((c) => {
        if (!q) return c.group !== "Commands" && c.group !== "Settings" && c.group !== "Workbench";
        const n = c.name.toLowerCase().replace(/^\//, "");
        return n.includes(q) || fuzzy(n) || (c.help || "").toLowerCase().includes(q);
      });
      // Best matches first: a name that starts with the query beats a
      // scattered one.
      if (q) palItems.sort((x, y) => (y.name.toLowerCase().replace(/^\//, "").startsWith(q) ? 1 : 0) - (x.name.toLowerCase().replace(/^\//, "").startsWith(q) ? 1 : 0));
      palItems = palItems.slice(0, q ? 14 : 24);
      palIdx = Math.min(palIdx, Math.max(palItems.length - 1, 0));
      let last = "";
      paletteList.innerHTML = palItems.length
        ? palItems.map((c, i) => {
          const head = !q && c.group !== last ? `<div class="pal-group">${esc(c.group)}</div>` : "";
          last = c.group;
          return head + `<div id="pal-option-${i}" role="option" aria-selected="${i === palIdx}" class="${i === palIdx ? "on" : ""}" data-i="${i}"><span class="name">${esc(c.name)}</span><span>${esc(c.help || "")}</span>${c.key ? `<kbd class="pal-key">${esc(c.key)}</kbd>` : ""}</div>`;
        }).join("")
        : `<div class="empty-pal">Nothing matches</div>`;
      const on = paletteList.querySelector(".on");
      if (on) {
        paletteQ.setAttribute("aria-activedescendant", on.id);
        on.scrollIntoView({ block: "nearest" });
      } else paletteQ.removeAttribute("aria-activedescendant");
    }
    async function runPaletteItem(c) {
      closePalette();
      if (!c) return;
      // Actions that open a menu or field take focus themselves; give the
      // palette's close a tick to finish first.
      await new Promise((r) => setTimeout(r, 0));
      await c.run();
    }
    document.getElementById("search-btn").addEventListener("click", openPalette);

    document.addEventListener("keydown", (ev) => {
      if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "k") {
        ev.preventDefault();
        if (palette.hidden) openPalette(); else closePalette();
      }
    });
    paletteQ.addEventListener("input", () => { palIdx = 0; drawPalette(paletteQ.value); });
    paletteQ.addEventListener("keydown", async (ev) => {
      if (ev.key === "Escape") { ev.preventDefault(); closePalette(); }
      if (ev.key === "ArrowDown") { ev.preventDefault(); palIdx = Math.min(palIdx + 1, palItems.length - 1); drawPalette(paletteQ.value); }
      if (ev.key === "ArrowUp") { ev.preventDefault(); palIdx = Math.max(palIdx - 1, 0); drawPalette(paletteQ.value); }
      if (ev.key === "Enter") {
        ev.preventDefault();
        await runPaletteItem(palItems[palIdx]);
      }
    });
    paletteList.addEventListener("click", async (ev) => {
      const row = ev.target.closest("[data-i]");
      if (!row) return;
      palIdx = Number(row.dataset.i);
      await runPaletteItem(palItems[palIdx]);
    });
    return { open: openPalette, close: closePalette, el: palette };
  };
})(window);
