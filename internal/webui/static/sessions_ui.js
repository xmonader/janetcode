// Session management, the context meter, the workspace picker, branch and
// rewind. Everything here talks to the /api/session/*, /api/workspace/*
// routes; app.js only calls the few hooks on window.sessionsUI.
(function (global) {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const esc = (t) => String(t == null ? "" : t).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const status = () => $("status");
  const say = (t) => { const el = status(); if (el) el.textContent = t; };
  const refresh = () => window.dispatchEvent(new Event("janet:sessions-refresh"));

  async function api(path, body) {
    try {
      const r = await fetch(path, { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(body || {}) });
      const text = await r.text();
      let data = null;
      try { data = JSON.parse(text); } catch (_) {}
      return { ok: r.ok, status: r.status, data, error: r.ok ? "" : (text || r.statusText).trim() };
    } catch (_) {
      return { ok: false, status: 0, data: null, error: "Cannot reach janetcode. Is it still running?" };
    }
  }

  // ---- Active / Archived tabs and the per-row state -----------------
  let tab = "active";
  try { if (localStorage.getItem("janetcode-sess-tab") === "archived") tab = "archived"; } catch (_) {}
  const seenKey = "janetcode-sess-seen";
  let seenMap = null;
  function loadSeen() {
    if (seenMap) return seenMap;
    try { seenMap = JSON.parse(localStorage.getItem(seenKey) || "null"); } catch (_) {}
    return seenMap;
  }
  function saveSeen() { try { localStorage.setItem(seenKey, JSON.stringify(seenMap)); } catch (_) {} }

  const tabs = document.createElement("div");
  tabs.className = "sess-tabs";
  tabs.setAttribute("role", "tablist");
  tabs.innerHTML = `<button type="button" id="sessions-tab-active" role="tab" data-tab="active" aria-controls="sessions">Active</button><button type="button" id="sessions-tab-archived" role="tab" data-tab="archived" aria-controls="sessions">Archived</button>`;
  tabs.setAttribute("aria-label", "Conversations");
  const list = $("sessions");
  if (list && list.parentNode) list.parentNode.insertBefore(tabs, list);
  if (list) list.setAttribute("role", "tabpanel");
  function paintTabs() {
    tabs.querySelectorAll("[data-tab]").forEach((b) => {
      const on = b.dataset.tab === tab;
      b.classList.toggle("on", on);
      b.setAttribute("aria-selected", on ? "true" : "false");
      b.tabIndex = on ? 0 : -1;
      if (on && list) list.setAttribute("aria-labelledby", b.id);
    });
  }
  paintTabs();
  tabs.addEventListener("click", (ev) => {
    const b = ev.target.closest("[data-tab]");
    if (!b) return;
    if (b.dataset.tab !== tab) {
      tab = b.dataset.tab;
      try { localStorage.setItem("janetcode-sess-tab", tab); } catch (_) {}
      paintTabs();
      refresh();
    } else paintTabs();
  });
  tabs.addEventListener("keydown", (ev) => {
    const rows = [...tabs.querySelectorAll("[data-tab]")];
    const at = rows.indexOf(document.activeElement);
    let next = -1;
    if (ev.key === "ArrowRight") next = (at + 1) % rows.length;
    else if (ev.key === "ArrowLeft") next = (at - 1 + rows.length) % rows.length;
    else if (ev.key === "Home") next = 0;
    else if (ev.key === "End") next = rows.length - 1;
    if (next < 0 || !rows[next]) return;
    ev.preventDefault();
    rows[next].click();
    rows[next].focus();
  });

  // seen is called with every listing: it marks the open conversation read
  // and, on the very first listing, everything already there (nothing is
  // "new" just because this browser never looked), and moves pins that an
  // older page kept in localStorage onto the server, once.
  function seen(rows) {
    let m = loadSeen();
    const first = !m;
    if (first) m = seenMap = {};
    for (const r of rows) if (first || r.current || m[r.id] == null) m[r.id] = r.updated;
    saveSeen();
    migratePins();
  }
  let migrated = false;
  async function migratePins() {
    if (migrated) return;
    migrated = true;
    let ids = [];
    try { ids = JSON.parse(localStorage.getItem("janetcode-pinned") || "[]"); } catch (_) {}
    if (!Array.isArray(ids) || !ids.length) return;
    const r = await api("/api/session/pin", { ids });
    if (r.ok) {
      try { localStorage.removeItem("janetcode-pinned"); } catch (_) {}
      refresh();
    }
  }

  function rowExtras(r) {
    const m = loadSeen() || {};
    const unread = !r.current && m[r.id] && r.updated && Date.parse(r.updated) > Date.parse(m[r.id]);
    const dot = r.running ? `<span class="sess-dot run" title="A turn is running" role="img" aria-label="running"></span>`
      : unread ? `<span class="sess-dot unread" title="New activity" role="img" aria-label="unread"></span>` : "";
    return dot + `<button type="button" class="sess-more" data-sess-menu="${esc(r.id)}" title="Rename, archive, delete" aria-label="Conversation actions" aria-haspopup="menu" aria-expanded="false">⋯</button>`;
  }

  // ---- row menu: rename, archive, delete ----------------------------
  let menu = null;
  let menuTrigger = null;
  function closeMenu(restore = false) {
    if (!menu) return;
    menu.remove();
    menu = null;
    if (menuTrigger) {
      menuTrigger.setAttribute("aria-expanded", "false");
      if (restore && menuTrigger.isConnected) menuTrigger.focus();
    }
    menuTrigger = null;
  }
  document.addEventListener("click", (ev) => { if (menu && !ev.target.closest(".sess-menu")) closeMenu(); }, true);
  document.addEventListener("keydown", (ev) => { if (ev.key === "Escape") { closeMenu(true); closePop(); } });

  function openMenu(btn) {
    closeMenu();
    const li = btn.closest("li[data-id]");
    if (!li) return;
    menuTrigger = btn;
    btn.setAttribute("aria-expanded", "true");
    const id = btn.dataset.sessMenu;
    const archived = tab === "archived";
    menu = document.createElement("div");
    menu.className = "sess-menu";
    menu.setAttribute("role", "menu");
    menu.innerHTML =
      `<button type="button" role="menuitem" data-act="rename">Rename</button>` +
      `<button type="button" role="menuitem" data-act="archive">${archived ? "Unarchive" : "Archive"}</button>` +
      `<button type="button" role="menuitem" data-act="delete" class="danger">Delete</button>`;
    document.body.appendChild(menu);
    const b = btn.getBoundingClientRect();
    menu.style.top = Math.min(window.innerHeight - menu.offsetHeight - 8, b.bottom + 4) + "px";
    menu.style.left = Math.max(8, Math.min(window.innerWidth - menu.offsetWidth - 8, b.left - menu.offsetWidth + b.width)) + "px";
    menu.dataset.id = id;
    menu.setAttribute("aria-label", "Conversation actions");
    menu.addEventListener("keydown", (ev) => {
      const rows = [...menu.querySelectorAll("button")];
      const at = rows.indexOf(document.activeElement);
      let next = -1;
      if (ev.key === "ArrowDown") next = (at + 1) % rows.length;
      else if (ev.key === "ArrowUp") next = (at - 1 + rows.length) % rows.length;
      else if (ev.key === "Home") next = 0;
      else if (ev.key === "End") next = rows.length - 1;
      else if (ev.key === "Tab") { closeMenu(true); return; }
      else return;
      ev.preventDefault();
      if (rows[next]) rows[next].focus();
    });
    menu.querySelector("button").focus();
  }

  document.addEventListener("click", async (ev) => {
    const item = ev.target.closest(".sess-menu [data-act]");
    if (!item) return;
    const id = menu.dataset.id;
    const act = item.dataset.act;
    const li = list.querySelector(`li[data-id="${CSS.escape(id)}"]`);
    closeMenu();
    if (act === "rename") return startRename(id, li);
    if (act === "archive") return report(await api("/api/session/archive", { id, archived: tab !== "archived" }));
    if (act === "delete") {
      const label = li ? li.querySelector("span").textContent.trim() : id;
      if (!window.confirm("Delete this conversation for good?\n\n" + label)) return;
      report(await api("/api/session/delete", { id }));
    }
  });

  function report(r) {
    if (!r.ok) say(r.error || "That did not work.");
    refresh();
  }

  function startRename(id, li) {
    if (!li) return;
    const span = li.querySelector("span");
    const input = document.createElement("input");
    input.type = "text";
    input.className = "sess-rename";
    input.value = span.textContent.trim();
    input.setAttribute("aria-label", "Conversation name");
    span.replaceWith(input);
    input.focus();
    input.select();
    let done = false;
    const finish = async (save) => {
      if (done) return;
      done = true;
      const title = input.value.trim();
      if (save && title) report(await api("/api/session/rename", { id, title }));
      else refresh();
    };
    input.addEventListener("click", (e) => e.stopPropagation());
    input.addEventListener("keydown", (e) => {
      if (e.key === "Enter") { e.preventDefault(); finish(true); }
      else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); finish(false); }
    });
    input.addEventListener("blur", () => finish(true));
  }

  // click is app.js's hook at the top of the list's click handler: true
  // when the click was ours.
  function click(ev) {
    const more = ev.target.closest("[data-sess-menu]");
    if (more) { ev.stopPropagation(); openMenu(more); return true; }
    if (ev.target.closest(".sess-rename")) return true;
    return false;
  }

  // Status changes (the stop button shows while a turn runs) refresh the
  // list so the running dot follows the turn.
  const stop = $("stop");
  if (stop) {
    let t = 0;
    new MutationObserver(() => { clearTimeout(t); t = setTimeout(refresh, 300); }).observe(stop, { attributes: true, attributeFilter: ["hidden"] });
  }

  // ---- popovers: shared plumbing ------------------------------------
  let pop = null;
  function closePop() { if (pop) { pop.remove(); pop = null; } }
  document.addEventListener("click", (ev) => {
    if (pop && !ev.target.closest(".jc-pop") && !ev.target.closest("#ctx-chip") && !ev.target.closest("#cwd")) closePop();
  }, true);
  function openPop(anchor, html, cls) {
    closePop();
    pop = document.createElement("div");
    pop.className = "jc-pop " + (cls || "");
    pop.setAttribute("role", "dialog");
    pop.innerHTML = html;
    document.body.appendChild(pop);
    const b = anchor.getBoundingClientRect();
    const w = pop.offsetWidth, h = pop.offsetHeight;
    pop.style.left = Math.max(8, Math.min(window.innerWidth - w - 8, b.right - w)) + "px";
    pop.style.top = (b.top - h - 8 > 8 ? b.top - h - 8 : b.bottom + 8) + "px";
    return pop;
  }

  // ---- context meter -------------------------------------------------
  const num = (n) => (n == null ? "—" : Number(n).toLocaleString());
  const chip = $("ctx-chip");
  if (chip) {
    chip.setAttribute("role", "button");
    chip.tabIndex = 0;
    const open = async () => {
      if (pop && pop.dataset.kind === "ctx") { closePop(); return; }
      let ws = null;
      try { ws = await (await fetch("/api/workspace")).json(); } catch (_) {}
      const c = ws && ws.ctx;
      if (!c) { say("No context numbers yet."); return; }
      const when = c.last_compact ? new Date(c.last_compact).toLocaleString() : "never";
      const el = openPop(chip,
        `<h3>Context</h3><dl>` +
        `<dt>Model</dt><dd>${esc(c.model || "—")}</dd>` +
        `<dt>Used</dt><dd>${num(c.used)} of ${c.limit ? num(c.limit) : "—"} tokens${c.limit ? " (" + c.pct + "%)" : ""}</dd>` +
        `</dl><div class="jc-bar" role="img" aria-label="${c.pct || 0} percent of the context used"><i style="width:${Math.min(100, c.pct || 0)}%"></i></div><dl>` +
        `<dt>Prompt tokens</dt><dd>${num(c.prompt_tokens)}</dd>` +
        `<dt>Completion tokens</dt><dd>${num(c.completion_tokens)}</dd>` +
        `<dt>Total this session</dt><dd>${num(c.total_tokens)}</dd>` +
        `<dt>Last compaction</dt><dd>${esc(when)}</dd></dl>`, "jc-ctx");
      el.dataset.kind = "ctx";
    };
    chip.addEventListener("click", open);
    chip.addEventListener("keydown", (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); open(); } });
  }

  // ---- workspace picker ----------------------------------------------
  const cwd = $("cwd");
  if (cwd) {
    cwd.setAttribute("role", "button");
    cwd.tabIndex = 0;
    cwd.style.cursor = "pointer";
    const open = () => {
      if (pop && pop.dataset.kind === "cwd") { closePop(); return; }
      const el = openPop(cwd,
        `<h3>Workspace</h3><form class="jc-form"><label>Folder<input type="text" id="wp-path" autocomplete="off" spellcheck="false" placeholder="/path/to/project or ~/project"></label>` +
        `<p class="jc-note" id="wp-note" role="status" aria-live="polite"></p>` +
        `<div class="jc-row"><button type="button" id="wp-cancel" class="ghost">Cancel</button><button type="submit" id="wp-apply" class="primary">Apply</button></div></form>`, "jc-cwd");
      el.dataset.kind = "cwd";
      const path = el.querySelector("#wp-path");
      const note = el.querySelector("#wp-note");
      const apply = el.querySelector("#wp-apply");
      path.value = cwd.title && cwd.title !== "Working directory" ? cwd.title : "";
      path.focus();
      path.select();
      let create = false;
      path.addEventListener("input", () => { create = false; note.textContent = ""; apply.textContent = "Apply"; });
      el.querySelector("#wp-cancel").addEventListener("click", closePop);
      el.querySelector("form").addEventListener("submit", async (ev) => {
        ev.preventDefault();
        const p = path.value.trim();
        if (!p) { note.textContent = "Type a folder."; return; }
        apply.disabled = true;
        // Check first: only a folder that exists is applied, and one that
        // does not is created only after a yes.
        const chk = await api("/api/workspace/check", { path: p });
        if (!chk.ok) { note.textContent = chk.error; note.className = "jc-note bad"; apply.disabled = false; return; }
        if (!chk.data.exists && !create) {
          create = true;
          note.className = "jc-note";
          note.textContent = "Create folder? " + chk.data.path + " does not exist.";
          apply.textContent = "Create folder";
          apply.disabled = false;
          return;
        }
        const r = await api("/api/workspace/apply", { path: p, create });
        apply.disabled = false;
        if (!r.ok) { note.textContent = r.error; note.className = "jc-note bad"; return; }
        closePop();
        say(r.data && r.data.status ? r.data.status : "Workspace changed");
        refresh();
      });
    };
    cwd.addEventListener("click", open);
    cwd.addEventListener("keydown", (ev) => { if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); open(); } });
  }

  // ---- branch and rewind ---------------------------------------------
  document.addEventListener("click", async (ev) => {
    const b = ev.target.closest("[data-branch]");
    if (!b) return;
    ev.stopPropagation();
    b.disabled = true;
    const r = await api("/api/session/branch", { entry: b.dataset.branch });
    b.disabled = false;
    if (!r.ok) { say(r.error); return; }
    say("Branched into a new conversation");
    refresh();
  }, true);

  // edit rewinds the conversation to before the message (the server cuts
  // the history, so the resent text replaces it instead of following it)
  // and offers to put the files back when a checkpoint of that turn exists.
  async function edit(id, src) {
    let info = null;
    try {
      const r = await fetch("/api/session/rewind?entry=" + encodeURIComponent(id));
      if (r.ok) info = await r.json();
    } catch (_) {}
    if (!info) return false;
    if (!window.confirm("Edit this message and resend it?\n\nThe conversation goes back to before it; the replies after it are removed.")) return true;
    let files = false;
    if (info.has_checkpoint) files = window.confirm("Also restore the files to how they were before this message (turn " + info.turn + ")?");
    const r = await api("/api/session/rewind", { entry: id, files });
    if (!r.ok) { say(r.error); return true; }
    const input = $("input") || document.querySelector("#composer textarea, textarea");
    if (input) {
      input.value = (r.data && r.data.text) || src.text || "";
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.focus();
    }
    say(r.data && r.data.restored ? "Rewound, files restored" : "Rewound to before that message");
    refresh();
    return true;
  }

  global.sessionsUI = { tab: () => tab, seen, rowExtras, click, edit };
})(window);
