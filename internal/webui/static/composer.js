// The composer: file chips, send, history (the up arrow), @-file
// autocomplete, the unsent-draft store, auto-grow, the slash menu and every
// key the textarea answers. Shared state: S.running (Ctrl+Enter steers a
// running turn), S.pageSession (the draft's key), S.commands (the slash
// menu's source), S.lastPrompt (what the retry button resends), S.pending
// and S.connected (the y/a/r/n/c shortcuts, the connect gate). Everything
// else here is private. app.js uses the returned helpers: attachments add
// mentions, the boot restores the draft, the palette focuses the input.
(function (global) {
  "use strict";
  global.JanetComposer = function ({ S, esc, icon, post, input, form, openConnect, chosenRule }) {
    const ac = document.getElementById("ac");
    let acItems = [];
    let acIdx = 0;
    let acStart = -1;
    let acTimer = 0;
    const chips = [];
    const chipsEl = document.getElementById("chips");
    function paintChips() {
      if (!chips.length) { chipsEl.hidden = true; chipsEl.innerHTML = ""; return; }
      chipsEl.hidden = false;
      chipsEl.innerHTML = chips.map((p, i) =>
        `<span class="file-chip">${esc(p.split("/").pop())}<button type="button" data-chip="${i}" aria-label="Remove">${icon("x")}</button></span>`
      ).join("");
    }
    chipsEl.addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-chip]");
      if (!b) return;
      chips.splice(Number(b.dataset.chip), 1);
      paintChips();
    });
    function composedText() {
      const typed = input.value.trim();
      const mentions = chips.map(mentionOf).join(" ");
      return [mentions, typed].filter(Boolean).join("\n");
    }
    form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (!slashEl.hidden) { acceptSlash(); return; }
      const text = composedText();
      if (!text) return;
      if (!S.connected && !text.startsWith("/")) {
        // Keep what they typed: it sends once a provider is connected.
        openConnect("Connect a provider first. Your message is still in the composer.");
        return;
      }
      if (!text.startsWith("/")) S.lastPrompt = text;
      histPush(text);
      const draft = input.value, attached = chips.slice();
      input.value = "";
      saveDraft();
      chips.length = 0;
      paintChips();
      resize();
      acHide();
      hideSlash();
      if (!(await post("/api/prompt", { text }))) {
        // Not sent: put it back rather than let it vanish. post has already
        // said why in the status line.
        if (!input.value) { input.value = draft; saveDraft(); }
        if (!chips.length) chips.push(...attached);
        paintChips();
        resize();
        input.focus();
      }
    });

    // Composer history: the prompts this browser has sent, recalled with
    // ↑ like the terminal's. The draft you had half-typed comes back on ↓
    // past the oldest entry.
    const hist = {
      items: JSON.parse(localStorage.getItem("janetcode-hist") || "[]"),
      idx: -1,
      draft: "",
    };
    function histPush(text) {
      if (!text || hist.items[hist.items.length - 1] === text) return;
      hist.items.push(text);
      if (hist.items.length > 200) hist.items.shift();
      try {
        localStorage.setItem("janetcode-hist", JSON.stringify(hist.items));
      } catch (_) {}
      hist.idx = -1;
    }
    // A path with a space in it is mentioned quoted, @"my notes.md", the
    // form the server's mention parser reads; bare it never attached.
    function mentionOf(p) {
      return /\s/.test(p) ? '@"' + p + '"' : "@" + p;
    }
    function acWord() {
      const pos = input.selectionStart;
      const before = input.value.slice(0, pos);
      const q = before.match(/(^|\s)@"([^"\n]*)$/);
      if (q) return { start: pos - q[2].length - 1, partial: q[2], quoted: true };
      const m = before.match(/(^|\s)@([^\s@"]*)$/);
      if (!m) return null;
      return { start: pos - m[2].length, partial: m[2], quoted: false };
    }
    function acHide() { ac.hidden = true; acItems = []; acStart = -1; }
    function acDraw() {
      ac.innerHTML = acItems.map((p, i) => {
        const dir = p.endsWith("/");
        return `<div class="${i === acIdx ? "on" : ""}" data-i="${i}">${dir ? "▸ " : ""}${esc(p)}</div>`;
      }).join("") || `<div class="ac-hint">no matches</div>`;
    }
    async function acRefresh() {
      const w = acWord();
      if (!w) { acHide(); return; }
      acStart = w.start;
      try {
        const items = await (await fetch("/api/complete?q=" + encodeURIComponent(w.partial))).json();
        if (acWord()?.partial !== w.partial) return;
        acItems = items || [];
        acIdx = 0;
        if (!acItems.length) { acHide(); return; }
        ac.hidden = false;
        acDraw();
      } catch (_) { acHide(); }
    }
    function acAccept() {
      const w = acWord();
      const p = acItems[acIdx];
      if (!w || !p) return;
      const tail = input.value.slice(input.selectionStart);
      const dir = p.endsWith("/");
      let text = p;
      // A directory's quote stays open so the path can go on.
      if (w.quoted || /\s/.test(p)) text = '"' + p + (dir ? "" : '"');
      const sep = dir ? "" : " ";
      input.value = input.value.slice(0, acStart) + text + sep + tail;
      input.selectionStart = input.selectionEnd = acStart + text.length + sep.length;
      acHide();
      resize();
      input.focus();
    }
    // The unsent prompt survives a reload or a closed tab. A session switch
    // or a send clears it; storage that throws just means no draft.
    // Drafts are per session: the key carries the session id. The old
    // global key is migrated into the first session that loads.
    const LEGACY_DRAFT_KEY = "janetcode-draft";
    const draftKey = () => LEGACY_DRAFT_KEY + (S.pageSession ? ":" + S.pageSession : "");
    function saveDraft() {
      try {
        if (input.value) localStorage.setItem(draftKey(), input.value);
        else localStorage.removeItem(draftKey());
      } catch (_) {}
    }
    function restoreDraft() {
      try {
        let d = localStorage.getItem(draftKey());
        if (!d && S.pageSession) {
          d = localStorage.getItem(LEGACY_DRAFT_KEY);
          if (d) { localStorage.setItem(draftKey(), d); localStorage.removeItem(LEGACY_DRAFT_KEY); }
        }
        if (d && !input.value) { input.value = d; resize(); }
      } catch (_) {}
    }
    input.addEventListener("input", () => {
      saveDraft();
      resize();
      slashIdx = 0;
      drawSlash();
      clearTimeout(acTimer);
      acTimer = setTimeout(acRefresh, 120);
    });
    ac.addEventListener("click", (ev) => {
      const d = ev.target.closest("[data-i]");
      if (!d) return;
      acIdx = Number(d.dataset.i);
      acAccept();
    });
    document.addEventListener("click", (ev) => {
      if (!ac.hidden && !ev.target.closest("#composer")) acHide();
    });

    function resize() {
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight, 140) + "px";
    }
    const slashEl = document.getElementById("slash");
    let slashItems = [];
    let slashIdx = 0;
    function hideSlash() { slashEl.hidden = true; slashItems = []; }
    function slashQuery() {
      const v = input.value;
      if (!v.startsWith("/") || /[\s\n]/.test(v)) return null;
      return v.toLowerCase();
    }
    function drawSlash() {
      const q = slashQuery();
      if (q == null) { hideSlash(); return; }
      acHide();
      slashItems = S.commands.filter((c) => {
        const n = (c.name || "").toLowerCase();
        if (!q || q === "/") return true;
        if (n.startsWith(q)) return true;
        let i = 0;
        const bare = q.slice(1);
        for (const ch of n.slice(1)) if (ch === bare[i] && ++i === bare.length) return true;
        return (c.help || "").toLowerCase().includes(bare);
      }).slice(0, 8);
      slashIdx = Math.min(slashIdx, Math.max(slashItems.length - 1, 0));
      if (!slashItems.length) { hideSlash(); return; }
      slashEl.hidden = false;
      slashEl.innerHTML = slashItems.map((c, i) =>
        `<div class="${i === slashIdx ? "on" : ""}" data-i="${i}"><span class="name">${esc(c.name)}</span><span>${esc(c.help || "")}</span></div>`
      ).join("");
    }
    async function acceptSlash() {
      const c = slashItems[slashIdx];
      hideSlash();
      if (!c) return;
      if (c.args) {
        input.value = c.name + " ";
        input.focus();
        resize();
        return;
      }
      input.value = "";
      resize();
      await post("/api/command", { text: c.name });
    }
    slashEl.addEventListener("click", async (ev) => {
      const row = ev.target.closest("[data-i]");
      if (!row) return;
      slashIdx = Number(row.dataset.i);
      await acceptSlash();
    });

    input.addEventListener("keydown", async (ev) => {
      if (!ac.hidden) {
        if (ev.key === "ArrowDown") { ev.preventDefault(); acIdx = Math.min(acIdx + 1, acItems.length - 1); acDraw(); return; }
        if (ev.key === "ArrowUp") { ev.preventDefault(); acIdx = Math.max(acIdx - 1, 0); acDraw(); return; }
        if (ev.key === "Enter" || ev.key === "Tab") { ev.preventDefault(); acAccept(); return; }
        if (ev.key === "Escape") { ev.preventDefault(); acHide(); return; }
      }
      if (!slashEl.hidden) {
        if (ev.key === "ArrowDown") { ev.preventDefault(); slashIdx = Math.min(slashIdx + 1, slashItems.length - 1); drawSlash(); return; }
        if (ev.key === "ArrowUp") { ev.preventDefault(); slashIdx = Math.max(slashIdx - 1, 0); drawSlash(); return; }
        if (ev.key === "Enter" && !ev.shiftKey) { ev.preventDefault(); await acceptSlash(); return; }
        if (ev.key === "Escape") { ev.preventDefault(); hideSlash(); return; }
      }
      if (ev.key === "Enter" && !ev.shiftKey && (ev.ctrlKey || ev.metaKey) && S.running) {
        ev.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        resize();
        await post("/api/steer", { text });
        return;
      }
      if (ev.key === "Backspace" && input.value === "" && chips.length) {
        chips.pop();
        paintChips();
        return;
      }
      if (ev.key === "Enter" && !ev.shiftKey) {
        ev.preventDefault();
        form.requestSubmit();
      }
      if (ev.key === "ArrowUp" && hist.items.length) {
        ev.preventDefault();
        if (hist.idx === -1) {
          hist.draft = input.value;
          hist.idx = hist.items.length - 1;
        } else if (hist.idx > 0) {
          hist.idx--;
        }
        input.value = hist.items[hist.idx];
        resize();
        return;
      }
      if (ev.key === "ArrowDown" && hist.idx !== -1) {
        ev.preventDefault();
        hist.idx++;
        if (hist.idx >= hist.items.length) {
          hist.idx = -1;
          input.value = hist.draft;
        } else {
          input.value = hist.items[hist.idx];
        }
        resize();
        return;
      }
      if (ev.key === "Escape") {
        ev.preventDefault();
        await post("/api/cancel", {});
      }
      if (S.pending && input.value === "" && !ev.metaKey && !ev.ctrlKey) {
        const map = { y: "allow", a: "session", r: "always", n: "deny", c: "cancel" };
        if (map[ev.key.toLowerCase()]) {
          ev.preventDefault();
          await post("/api/permission", { decision: map[ev.key.toLowerCase()], rule: chosenRule(), id: S.pending.id });
        }
      }
      if (ev.key === "s" && ev.ctrlKey && S.running) {
        ev.preventDefault();
        const text = input.value.trim();
        if (!text) return;
        input.value = "";
        await post("/api/steer", { text });
      }
    });
    return { resize, saveDraft, restoreDraft, mentionOf, slashEl, hideSlash };
  };
})(window);
