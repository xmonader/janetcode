// Frontend extras that need no backend: quick prompts, artifact preview,
// attachment thumbnails and the conversation-files gallery, connection
// recovery, the conversation toolbar, the "Stopped" tag, the export dialog,
// extra settings, palette actions and modal/keyboard accessibility.
// app.js reaches this file only through window.JanetExtras (toolDefault,
// uploaded, actions, openExport); this file reaches the page only through
// window.JanetApp. Loaded after app.js.
(function () {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const store = {
    get(k, d) { try { const v = localStorage.getItem(k); return v == null ? d : v; } catch (_) { return d; } },
    set(k, v) { try { localStorage.setItem(k, v); } catch (_) {} },
  };
  const ico = (n) => `<svg class="i" aria-hidden="true"><use href="#i-${n}"/></svg>`;
  const App = () => window.JanetApp || {};
  const input = $("input");
  const log = $("log");

  // ---------------------------------------------------------------- dialogs
  // A native <dialog>: modal, focus-trapped, Esc closes, focus returns to
  // whatever opened it.
  let dlgSeq = 0;
  function dialog(cls, title, bodyHTML) {
    const d = document.createElement("dialog");
    const tid = "xd-title-" + ++dlgSeq;
    d.className = "xdlg " + cls;
    d.dataset.testid = "dialog-" + cls;
    d.setAttribute("aria-labelledby", tid);
    d.innerHTML = `<div class="xd-card"><header><strong id="${tid}">${esc(title)}</strong>` +
      `<button type="button" class="icon-btn xd-close" aria-label="Close" title="Close (Esc)">${ico("x")}</button></header>` +
      `<div class="xd-body">${bodyHTML}</div></div>`;
    document.body.appendChild(d);
    d.querySelector(".xd-close").addEventListener("click", () => d.close());
    d.addEventListener("close", () => d.remove());
    d.addEventListener("click", (ev) => { if (ev.target === d) d.close(); });
    // Keys inside belong to the dialog, not to the page's shortcuts.
    d.addEventListener("keydown", (ev) => { if (ev.key !== "Escape") ev.stopPropagation(); });
    d.showModal();
    return d;
  }
  const anyDialog = () => document.querySelector("dialog.xdlg[open]");

  // ---------------------------------------------------------- quick prompts
  const QP_KEY = "janetcode-quick-prompts";
  const QP_DEFAULTS = [
    { id: "d1", title: "Explain this code", text: "Explain how this code works, step by step, and point out anything surprising." },
    { id: "d2", title: "Review my changes", text: "Review the uncommitted changes for bugs, missing tests and unclear naming. Be specific." },
    { id: "d3", title: "Add tests", text: "Add tests for the code we just changed. Cover the edge cases, not just the happy path." },
  ];
  function qpLoad() {
    try {
      const a = JSON.parse(store.get(QP_KEY, "null"));
      if (Array.isArray(a)) return a.filter((x) => x && typeof x.text === "string");
    } catch (_) {}
    return QP_DEFAULTS.map((x) => Object.assign({}, x));
  }
  function qpSave(list) { store.set(QP_KEY, JSON.stringify(list)); }

  function openQuickPrompts() {
    if (anyDialog()) return;
    const d = dialog("qp", "Quick prompts",
      `<input type="search" class="qp-q" placeholder="Search prompts" aria-label="Search prompts" autocomplete="off">` +
      `<ul class="qp-list" role="listbox" aria-label="Saved prompts"></ul>` +
      `<form class="qp-form" hidden><label>Title<input class="qp-title" maxlength="80" autocomplete="off"></label>` +
      `<label>Prompt<textarea class="qp-text" rows="5"></textarea></label>` +
      `<div class="xd-actions"><button type="button" class="ghost qp-cancel">Cancel</button><button type="submit" class="primary">Save</button></div></form>` +
      `<div class="xd-actions qp-foot"><span class="xd-hint">Enter inserts · Ctrl P opens this</span><button type="button" class="qp-new">New prompt</button></div>`);
    const q = d.querySelector(".qp-q"), ul = d.querySelector(".qp-list"), form = d.querySelector(".qp-form");
    const fTitle = d.querySelector(".qp-title"), fText = d.querySelector(".qp-text");
    let list = qpLoad(), editing = null;
    function draw() {
      const f = q.value.trim().toLowerCase();
      const rows = list.filter((p) => !f || (p.title + " " + p.text).toLowerCase().includes(f));
      ul.innerHTML = rows.length ? rows.map((p) =>
        `<li data-id="${esc(p.id)}"><button type="button" class="qp-use" role="option"><b>${esc(p.title || p.text.slice(0, 40))}</b><span>${esc(p.text.slice(0, 140))}</span></button>` +
        `<button type="button" class="icon-btn qp-edit" aria-label="Edit ${esc(p.title)}" title="Edit">${ico("pencil")}</button>` +
        `<button type="button" class="icon-btn qp-del" aria-label="Delete ${esc(p.title)}" title="Delete">${ico("x")}</button></li>`
      ).join("") : `<li class="xd-empty">${list.length ? "Nothing matches." : "No saved prompts yet. Add one below."}</li>`;
    }
    function insert(p) {
      d.close();
      if (p && App().insertComposer) App().insertComposer(p.text);
    }
    function edit(p) {
      editing = p ? p.id : "";
      fTitle.value = p ? p.title : "";
      fText.value = p ? p.text : "";
      form.hidden = false;
      (p ? fText : fTitle).focus();
    }
    q.addEventListener("input", draw);
    q.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        const f = q.value.trim().toLowerCase();
        insert(list.find((p) => !f || (p.title + " " + p.text).toLowerCase().includes(f)));
      } else if (ev.key === "ArrowDown") { ev.preventDefault(); const b = ul.querySelector(".qp-use"); if (b) b.focus(); }
    });
    ul.addEventListener("keydown", (ev) => {
      const li = ev.target.closest("li");
      if (!li) return;
      if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
        ev.preventDefault();
        const sib = ev.key === "ArrowDown" ? li.nextElementSibling : li.previousElementSibling;
        const b = sib && sib.querySelector(".qp-use");
        if (b) b.focus(); else if (ev.key === "ArrowUp") q.focus();
      }
    });
    ul.addEventListener("click", (ev) => {
      const li = ev.target.closest("li[data-id]");
      if (!li) return;
      const p = list.find((x) => x.id === li.dataset.id);
      if (!p) return;
      if (ev.target.closest(".qp-del")) { list = list.filter((x) => x !== p); qpSave(list); draw(); }
      else if (ev.target.closest(".qp-edit")) edit(p);
      else insert(p);
    });
    d.querySelector(".qp-new").addEventListener("click", () => edit(null));
    d.querySelector(".qp-cancel").addEventListener("click", () => { form.hidden = true; q.focus(); });
    form.addEventListener("submit", (ev) => {
      ev.preventDefault();
      const text = fText.value.trim();
      if (!text) { fText.focus(); return; }
      const title = fTitle.value.trim() || text.slice(0, 40);
      const cur = editing && list.find((x) => x.id === editing);
      if (cur) { cur.title = title; cur.text = text; }
      else list.push({ id: Date.now().toString(36) + Math.random().toString(36).slice(2, 6), title, text });
      qpSave(list);
      form.hidden = true;
      draw();
      q.focus();
    });
    draw();
    q.focus();
  }
  document.addEventListener("keydown", (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && !ev.shiftKey && !ev.altKey && ev.key.toLowerCase() === "p") {
      ev.preventDefault();
      openQuickPrompts();
    }
  }, true);
  (function addQpButton() {
    const after = $("search-btn");
    if (!after) return;
    const b = document.createElement("button");
    b.type = "button"; b.id = "qp-btn"; b.className = "cmd-btn";
    b.title = "Quick prompts (Ctrl+P)"; b.setAttribute("aria-label", "Quick prompts");
    b.innerHTML = ico("file-plus");
    b.addEventListener("click", openQuickPrompts);
    after.after(b);
  })();

  // ------------------------------------------------------- artifact preview
  const PREVIEW_LANGS = { html: "html", htm: "html", svg: "svg", markdown: "md", md: "md", csv: "csv" };

  function parseCSV(src) {
    const rows = [];
    let row = [], cell = "", q = false;
    for (let i = 0; i < src.length; i++) {
      const c = src[i];
      if (q) {
        if (c === '"') { if (src[i + 1] === '"') { cell += '"'; i++; } else q = false; }
        else cell += c;
      } else if (c === '"') q = true;
      else if (c === ",") { row.push(cell); cell = ""; }
      else if (c === "\n" || c === "\r") {
        if (c === "\r" && src[i + 1] === "\n") i++;
        row.push(cell); cell = "";
        rows.push(row); row = [];
      } else cell += c;
    }
    if (cell !== "" || row.length) { row.push(cell); rows.push(row); }
    return rows.filter((r) => r.some((x) => x !== ""));
  }
  function csvTable(src) {
    const rows = parseCSV(src), MAX = 500;
    const wrap = document.createElement("div");
    wrap.className = "xd-csv";
    if (!rows.length) { wrap.textContent = "Empty CSV."; return wrap; }
    const t = document.createElement("table");
    const head = document.createElement("thead"), body = document.createElement("tbody");
    const mk = (tag, r) => { const tr = document.createElement("tr"); r.forEach((v) => { const c = document.createElement(tag); c.textContent = v; tr.appendChild(c); }); return tr; };
    head.appendChild(mk("th", rows[0]));
    rows.slice(1, MAX + 1).forEach((r) => body.appendChild(mk("td", r)));
    t.append(head, body);
    wrap.appendChild(t);
    if (rows.length - 1 > MAX) {
      const p = document.createElement("p");
      p.className = "xd-hint";
      p.textContent = "Showing the first " + MAX + " of " + (rows.length - 1) + " rows.";
      wrap.appendChild(p);
    }
    return wrap;
  }
  // The document an HTML preview runs: the CSP meta comes first and allows
  // no network at all; the iframe is sandboxed with no same-origin, so
  // nothing in it can reach the page, its cookies or its API.
  function htmlDoc(src, scripts) {
    const csp = "default-src 'none'; img-src data: blob:; media-src data: blob:; font-src data:; style-src 'unsafe-inline'; " +
      (scripts ? "script-src 'unsafe-inline'" : "script-src 'none'");
    return '<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="' + csp + '"></head><body>' + src + "</body></html>";
  }
  function svgDoc(src) {
    return htmlDoc('<div style="display:grid;place-items:center;min-height:90vh"><img alt="SVG preview" style="max-width:100%;max-height:90vh" src="data:image/svg+xml;charset=utf-8,' + encodeURIComponent(src) + '"></div>', false);
  }
  function frameFor(doc, scripts) {
    const f = document.createElement("iframe");
    f.setAttribute("sandbox", scripts ? "allow-scripts" : "");
    f.setAttribute("referrerpolicy", "no-referrer");
    f.title = "Artifact preview";
    f.srcdoc = doc;
    return f;
  }
  function openArtifact(kind, src, label) {
    if (anyDialog()) return;
    const d = dialog("art", "Preview · " + (label || kind),
      `<div class="art-bar"><span class="xd-hint art-note"></span><span class="spacer"></span>` +
      `<label class="art-run" hidden><input type="checkbox"> Run scripts</label><button type="button" class="art-copy">Copy source</button></div>` +
      `<div class="art-stage"></div>`);
    const stage = d.querySelector(".art-stage"), note = d.querySelector(".art-note");
    const runL = d.querySelector(".art-run"), run = runL.querySelector("input");
    d.querySelector(".art-copy").addEventListener("click", async (ev) => {
      try { await navigator.clipboard.writeText(src); ev.target.textContent = "Copied"; setTimeout(() => { ev.target.textContent = "Copy source"; }, 1500); } catch (_) {}
    });
    function paint() {
      stage.textContent = "";
      if (kind === "html") {
        runL.hidden = false;
        note.textContent = run.checked ? "Sandboxed, scripts on, no network." : "Sandboxed, scripts off, no network.";
        stage.appendChild(frameFor(htmlDoc(src, run.checked), run.checked));
      } else if (kind === "svg") {
        note.textContent = "Drawn as an image: scripts in an SVG never run.";
        stage.appendChild(frameFor(svgDoc(src), false));
      } else if (kind === "md") {
        const div = document.createElement("div");
        div.className = "md xd-md";
        div.innerHTML = window.JanetMD ? JanetMD.render(src, true) : "";
        if (!window.JanetMD) div.textContent = src;
        stage.appendChild(div);
      } else {
        stage.appendChild(csvTable(src));
      }
    }
    run.addEventListener("change", paint);
    paint();
    d.querySelector(".art-copy").focus();
  }
  document.addEventListener("click", (ev) => {
    const b = ev.target.closest(".codepreview");
    if (!b) return;
    const src = window.JanetMD && JanetMD.codeBlocks.get(b.dataset.bid);
    if (src == null) return;
    const lang = (b.closest(".codeblock").dataset.lang || "").toLowerCase();
    openArtifact(PREVIEW_LANGS[lang], src, lang);
  });
  function decorateCode() {
    log.querySelectorAll(".codeblock:not([data-px])").forEach((cb) => {
      cb.dataset.px = "1";
      const kind = PREVIEW_LANGS[(cb.dataset.lang || "").toLowerCase()];
      const copy = cb.querySelector(".codecopy[data-bid]");
      if (!kind || !copy) return;
      const b = document.createElement("button");
      b.type = "button"; b.className = "codepreview"; b.dataset.bid = copy.dataset.bid;
      b.title = "Preview this " + (cb.dataset.lang || "") + " in a sandbox"; b.textContent = "Preview";
      copy.before(b);
    });
  }

  // ------------------------------------------------------------ attachments
  const uploads = new Map(); // path -> { name, size, type, url, file }
  const fmtSize = (n) => n == null ? "" : n < 1024 ? n + " B" : n < 1048576 ? (n / 1024).toFixed(n < 10240 ? 1 : 0) + " KB" : (n / 1048576).toFixed(1) + " MB";
  const mentionOf = (p) => (/\s/.test(p) ? '@"' + p + '"' : "@" + p);
  const baseName = (p) => String(p).split("/").pop();

  function uploaded(files, paths) {
    paths.forEach((p, i) => {
      const f = files[i];
      if (!f) return;
      const prev = uploads.get(p);
      if (prev && prev.url) URL.revokeObjectURL(prev.url);
      uploads.set(p, { name: f.name || baseName(p), size: f.size, type: f.type || "", file: f, url: /^image\//.test(f.type) ? URL.createObjectURL(f) : "" });
    });
    paintTray();
  }

  const tray = document.createElement("div");
  tray.id = "att-tray"; tray.className = "att-tray"; tray.hidden = true;
  const chipsEl = $("chips");
  if (chipsEl) chipsEl.before(tray);
  let traySig = "";
  function paintTray() {
    const v = input.value;
    const live = [...uploads].filter(([p]) => v.includes(mentionOf(p)));
    const sig = live.map(([p]) => p).join("\n");
    if (sig === traySig) return;
    traySig = sig;
    tray.hidden = !live.length;
    tray.innerHTML = live.map(([p, u]) =>
      `<span class="att-chip"><button type="button" class="att-open" data-p="${esc(p)}" title="Preview ${esc(u.name)}">` +
      (u.url ? `<img src="${esc(u.url)}" alt="">` : `<span class="att-ico">${ico("file")}</span>`) +
      `<span class="att-name">${esc(u.name)}</span><span class="att-size">${fmtSize(u.size)}</span></button>` +
      `<button type="button" class="att-x" data-p="${esc(p)}" aria-label="Remove ${esc(u.name)}">${ico("x")}</button></span>`
    ).join("");
  }
  tray.addEventListener("click", (ev) => {
    const x = ev.target.closest(".att-x");
    if (x) {
      const m = mentionOf(x.dataset.p);
      input.value = input.value.split(m).join("").replace(/ {2,}/g, " ").replace(/^ +/, "");
      input.dispatchEvent(new Event("input"));
      return;
    }
    const o = ev.target.closest(".att-open");
    if (o) previewAttachment(o.dataset.p);
  });
  input.addEventListener("input", paintTray);
  $("composer").addEventListener("submit", () => setTimeout(paintTray, 0));
  setInterval(paintTray, 1000);

  function previewAttachment(path) {
    if (anyDialog() && !anyDialog().classList.contains("gal")) return;
    const u = uploads.get(path);
    const d = dialog("att", baseName(path),
      `<p class="xd-hint">${esc(path)}${u && u.size != null ? " · " + fmtSize(u.size) : ""}</p><div class="att-view"></div>`);
    const view = d.querySelector(".att-view");
    if (u && u.url) {
      const img = document.createElement("img");
      img.alt = u.name; img.src = u.url;
      view.appendChild(img);
    } else if (u && u.file && (/^text\//.test(u.type) || /\.(txt|md|json|csv|go|js|ts|py|rs|html|css|ya?ml|toml|sh)$/i.test(u.name))) {
      u.file.slice(0, 40000).text().then((t) => { const pre = document.createElement("pre"); pre.textContent = t; view.appendChild(pre); });
    } else {
      view.innerHTML = `<p class="xd-empty">${u ? "No inline preview for this file type." : "This file was attached earlier; its preview is only kept while the page that uploaded it stays open."}</p>`;
    }
  }

  function attachmentList() {
    const out = new Map();
    const re = /@"([^"\n]+)"|@([^\s@"]+)/g;
    for (const e of App().entries ? App().entries() : []) {
      if (e.kind !== "user") continue;
      let m;
      while ((m = re.exec(e.text || ""))) {
        const p = (m[1] || m[2]).replace(/[.,;:!?)]+$/, "");
        if (!/[./]/.test(p)) continue;
        if (!out.has(p)) out.set(p, { path: p, uploaded: p.includes(".janetcode/uploads/") });
      }
    }
    for (const [p, u] of uploads) {
      if (!out.has(p)) out.set(p, { path: p, uploaded: true });
    }
    return [...out.values()];
  }
  function openGallery() {
    if (anyDialog()) return;
    const files = attachmentList();
    const d = dialog("gal", "Conversation files",
      files.length
        ? `<div class="gal-grid">${files.map((f) => {
          const u = uploads.get(f.path);
          return `<button type="button" class="gal-item" data-p="${esc(f.path)}">` +
            (u && u.url ? `<img src="${esc(u.url)}" alt="">` : `<span class="gal-ico">${ico("file")}</span>`) +
            `<b>${esc(baseName(f.path))}</b><span>${esc(f.path.includes("/") ? f.path.slice(0, f.path.lastIndexOf("/")) : "")}</span>` +
            `<span>${f.uploaded ? "uploaded" : "mentioned"}${u && u.size != null ? " · " + fmtSize(u.size) : ""}</span></button>`;
        }).join("")}</div>`
        : `<p class="xd-empty">No attachments or file mentions in this conversation yet. Attach a file with the paperclip or mention one with @.</p>`);
    d.querySelectorAll(".gal-item").forEach((b) => b.addEventListener("click", () => previewAttachment(b.dataset.p)));
    const first = d.querySelector(".gal-item");
    if (first) first.focus();
  }

  // ----------------------------------------------------- connection recovery
  const conn = $("conn");
  const bar = document.createElement("div");
  bar.id = "recover-bar"; bar.className = "recover-bar"; bar.setAttribute("role", "alert"); bar.hidden = true;
  bar.innerHTML = `<span class="rb-msg">Connection lost. Reconnecting automatically. Your draft is kept.</span><button type="button" class="rb-retry">Retry</button>`;
  const statusEl = $("status");
  if (statusEl) statusEl.before(bar);
  const rbMsg = bar.querySelector(".rb-msg");
  function syncConn() {
    const off = conn.classList.contains("off");
    if (off && bar.hidden) rbMsg.textContent = "Connection lost. Reconnecting automatically. Your draft is kept.";
    bar.hidden = !off;
  }
  new MutationObserver(syncConn).observe(conn, { attributes: true, attributeFilter: ["class"] });
  bar.querySelector(".rb-retry").addEventListener("click", async () => {
    rbMsg.textContent = "Trying again…";
    try {
      const r = await fetch("/api/state", { cache: "no-store" });
      if (r.status === 401) { location.reload(); return; }
      if (!r.ok) throw new Error(String(r.status));
      const es = window.__es;
      if (es && es.readyState === 2) { location.reload(); return; } // the stream gave up; the draft is saved
      rbMsg.textContent = "The server answers. Waiting for the live stream… Your draft is kept.";
    } catch (_) {
      rbMsg.textContent = "Still unreachable. Your draft is kept; try again in a moment.";
    }
  });
  addEventListener("beforeunload", (ev) => {
    const stop = $("stop");
    const running = stop && !stop.hidden;
    if (running || input.value.trim()) {
      ev.preventDefault();
      ev.returnValue = "";
    }
  });

  // ------------------------------------------------------- conversation bar
  const XF = [["all", "All tools"], ["read", "Reads"], ["change", "Changes"], ["search", "Searches"], ["list", "Lists"], ["cmd", "Commands"], ["other", "Other"]];
  function xcatOf(name) {
    const n = String(name || "").toLowerCase();
    if (/^(edit_file|edit|multi_edit|apply_patch|replace|write_file|write|create_file)$/.test(n)) return "change";
    if (/^(bash|shell|run|job|jobs)$/.test(n)) return "cmd";
    if (/^(list_dir|ls|glob|tree)$/.test(n)) return "list";
    if (/grep|search|find|references|definition|symbols|repo_map/.test(n)) return "search";
    if (/^(read_file|read|read_many|expand_output)$/.test(n)) return "read";
    return "other";
  }
  function categorize() {
    const nodes = log.querySelectorAll(".tool-turn:not([data-xcat])");
    if (!nodes.length) return;
    const byId = new Map();
    for (const e of App().entries ? App().entries() : []) if (e.kind === "tool") byId.set(e.id, e);
    nodes.forEach((n) => { const e = byId.get(n.dataset.id); if (e) n.dataset.xcat = xcatOf(e.name); });
  }
  const tb = document.createElement("div");
  tb.id = "convo-bar"; tb.className = "convo-bar"; tb.setAttribute("role", "toolbar"); tb.setAttribute("aria-label", "Conversation tools");
  tb.innerHTML = `<label class="cb-lbl">Tools <select id="tool-filter" aria-label="Filter tool calls">` +
    XF.map(([v, l]) => `<option value="${v}">${l}</option>`).join("") + `</select></label>` +
    `<button type="button" id="tools-expand">Expand all</button><button type="button" id="tools-collapse">Collapse all</button>` +
    `<span class="spacer"></span>` +
    `<button type="button" id="convo-files" title="Files attached to this conversation">Attachments</button>` +
    `<button type="button" id="convo-find" title="Find in conversation (Ctrl+F)">${ico("search")} Find</button>`;
  if (log) log.before(tb);
  $("tool-filter").addEventListener("change", (ev) => {
    if (ev.target.value === "all") log.removeAttribute("data-xf"); else log.dataset.xf = ev.target.value;
  });
  function bulk(open) {
    log.querySelectorAll(".tool-group").forEach((g) => {
      if (open && g.classList.contains("folded")) g.querySelector(".tg-toggle").click();
      const b = g.querySelector(open ? ".tg-expand" : ".tg-collapse");
      if (b) b.click();
    });
  }
  $("tools-expand").addEventListener("click", () => bulk(true));
  $("tools-collapse").addEventListener("click", () => bulk(false));
  $("convo-find").addEventListener("click", () => App().openFind && App().openFind());
  $("convo-files").addEventListener("click", openGallery);

  // --------------------------------------------------------- stopped marker
  function markStopped() {
    log.querySelectorAll(".turn.note:not([data-stopscan])").forEach((note) => {
      note.dataset.stopscan = "1";
      if (!/^stopped after/i.test(note.textContent || "")) return;
      for (let p = note.previousElementSibling; p; p = p.previousElementSibling) {
        if (p.classList.contains("you")) return;
        if (p.tagName === "ARTICLE" && p.querySelector(":scope > .md")) { p.dataset.stopped = "1"; return; }
      }
    });
    log.querySelectorAll("[data-stopped]").forEach((a) => {
      if (!a.querySelector(".stopped-tag")) {
        const s = document.createElement("span");
        s.className = "stopped-tag"; s.textContent = "Stopped"; s.title = "You stopped this reply before it finished";
        a.appendChild(s);
      }
    });
  }

  let scanQueued = false;
  new MutationObserver(() => {
    if (scanQueued) return;
    scanQueued = true;
    requestAnimationFrame(() => {
      scanQueued = false;
      decorateCode();
      categorize();
      markStopped();
    });
  }).observe(log, { childList: true, subtree: true });

  // ----------------------------------------------------------------- export
  function openExport() {
    if (anyDialog()) return;
    let saved = {};
    try { saved = JSON.parse(store.get("janetcode-export", "{}")) || {}; } catch (_) {}
    const on = (k, d) => (k in saved ? !!saved[k] : d);
    const d = dialog("exp", "Export chat",
      `<p class="xd-hint">Choose what goes into the Markdown file.</p>` +
      `<label class="xd-check"><input type="checkbox" data-k="reasoning"${on("reasoning", false) ? " checked" : ""}> Include reasoning</label>` +
      `<label class="xd-check"><input type="checkbox" data-k="tools"${on("tools", true) ? " checked" : ""}> Include tool summaries</label>` +
      `<label class="xd-check xd-sub"><input type="checkbox" data-k="output"${on("output", false) ? " checked" : ""}> and their full output and diffs</label>` +
      `<label class="xd-check"><input type="checkbox" data-k="manifest"${on("manifest", false) ? " checked" : ""}> Include an attachment manifest</label>` +
      `<div class="xd-actions"><button type="button" class="ghost exp-copy">Copy</button><span class="spacer"></span><button type="button" class="ghost xd-cancel">Cancel</button><button type="button" class="primary exp-save">Save .md</button></div>`);
    const val = () => {
      const o = {};
      d.querySelectorAll("input[data-k]").forEach((c) => { o[c.dataset.k] = c.checked; });
      return o;
    };
    const build = () => {
      const o = val();
      store.set("janetcode-export", JSON.stringify(o));
      let md = App().chatMarkdown ? App().chatMarkdown({ reasoning: o.reasoning, tools: o.tools || o.output, output: o.output }) : "";
      if (o.manifest) {
        const fl = attachmentList().map((f) => { const u = uploads.get(f.path); return "- `" + f.path + "`" + (u && u.size != null ? " (" + fmtSize(u.size) + ")" : "") + (f.uploaded ? " uploaded" : " mentioned"); });
        md += "\n\n## Attachments\n\n" + (fl.length ? fl.join("\n") : "None.");
      }
      return md;
    };
    d.querySelector(".xd-cancel").addEventListener("click", () => d.close());
    d.querySelector(".exp-save").addEventListener("click", () => { const md = build(); d.close(); if (App().downloadMarkdown) App().downloadMarkdown(md); });
    d.querySelector(".exp-copy").addEventListener("click", async (ev) => {
      try { await navigator.clipboard.writeText(build()); ev.target.textContent = "Copied"; } catch (_) {}
    });
    d.querySelector(".exp-save").focus();
  }

  // --------------------------------------------------------------- settings
  const quietOn = () => store.get("janetcode-quiet", "") === "1";
  function setQuiet(v) {
    store.set("janetcode-quiet", v ? "1" : "");
    const b = $("quiet-btn");
    if (b) b.setAttribute("aria-checked", v ? "true" : "false");
  }
  const wrapOn = () => store.get("janetcode-wrap-code", "") === "1";
  function setWrap(v) {
    store.set("janetcode-wrap-code", v ? "1" : "");
    document.documentElement.toggleAttribute("data-wrap-code", v);
    const b = $("wrap-btn");
    if (b) b.setAttribute("aria-checked", v ? "true" : "false");
  }
  const toolDefault = () => { const v = store.get("janetcode-tool-open", "auto"); return v === "open" || v === "collapsed" ? v : "auto"; };
  const layoutMode = () => { const v = store.get("janetcode-work-layout", "auto"); return v === "open" || v === "closed" ? v : "auto"; };
  const shell = document.querySelector(".shell");
  function enforceLayout() {
    const m = layoutMode();
    if (m === "auto" || !shell) return;
    if (m === "open" && matchMedia("(max-width: 960px)").matches) return;
    const want = m === "closed";
    if (shell.classList.contains("work-collapsed") !== want) shell.classList.toggle("work-collapsed", want);
  }
  if (shell) new MutationObserver(enforceLayout).observe(shell, { attributes: true, attributeFilter: ["class"] });

  function seg(id, attr, opts, cur) {
    return `<div class="seg" id="${id}">` + opts.map(([v, l]) => `<button type="button" data-${attr}="${v}"${v === cur ? ' class="on"' : ""}>${l}</button>`).join("") + `</div>`;
  }
  (function extendSettings() {
    const card = document.querySelector("#settings .settings-card");
    if (!card) return;
    const sec = document.createElement("section");
    sec.className = "settings-extra";
    sec.innerHTML =
      `<h3>Code blocks</h3><label class="switch-row" for="wrap-btn"><span>Wrap long lines</span><button type="button" id="wrap-btn" class="switch" role="switch" aria-checked="false"></button></label>` +
      `<h3>Tool calls</h3>` + seg("toolopen-seg", "tool", [["auto", "Edits open"], ["open", "All open"], ["collapsed", "All collapsed"]], toolDefault()) +
      `<h3>Workbench panel</h3>` + seg("layout-seg", "layout", [["auto", "Automatic"], ["open", "Always open"], ["closed", "Always closed"]], layoutMode()) +
      `<h3>More</h3><div class="settings-links"><button type="button" id="set-providers">Providers…</button><button type="button" id="set-shortcuts">Keyboard shortcuts</button></div>`;
    card.appendChild(sec);
    // Quiet mode is a notification setting: it sits with the sound switch.
    const snd = $("sound-btn") && $("sound-btn").closest("label");
    const quiet = `<label class="switch-row" for="quiet-btn"><span>Quiet mode: no sound, no notifications</span><button type="button" id="quiet-btn" class="switch" role="switch" aria-checked="false"></button></label>`;
    if (snd) snd.insertAdjacentHTML("afterend", quiet); else sec.insertAdjacentHTML("afterbegin", quiet);
    setWrap(wrapOn()); setQuiet(quietOn());
    $("wrap-btn").addEventListener("click", () => setWrap(!wrapOn()));
    $("quiet-btn").addEventListener("click", () => setQuiet(!quietOn()));
    $("toolopen-seg").addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-tool]");
      if (!b) return;
      store.set("janetcode-tool-open", b.dataset.tool);
      $("toolopen-seg").querySelectorAll("button").forEach((x) => x.classList.toggle("on", x === b));
      if (App().rerenderTools) App().rerenderTools();
    });
    $("layout-seg").addEventListener("click", (ev) => {
      const b = ev.target.closest("[data-layout]");
      if (!b) return;
      store.set("janetcode-work-layout", b.dataset.layout);
      $("layout-seg").querySelectorAll("button").forEach((x) => x.classList.toggle("on", x === b));
      enforceLayout();
    });
    $("set-providers").addEventListener("click", () => { $("settings").hidden = true; if (App().openConnect) App().openConnect(); });
    $("set-shortcuts").addEventListener("click", () => { $("settings").hidden = true; if (App().openHelp) App().openHelp(); });
  })();
  setWrap(wrapOn());
  enforceLayout();
  (function helpRows() {
    const dl = document.querySelector("#help .help-cols dl");
    if (!dl) return;
    dl.insertAdjacentHTML("beforeend", `<dt><kbd>Ctrl P</kbd></dt><dd>quick prompts</dd>`);
  })();

  // -------------------------------------------------------- palette actions
  function actions(act) {
    return [
      act("Actions", "Export chat", "choose what to include, save as Markdown", openExport),
      act("Actions", "Conversation files", "attachments and uploads in this chat", openGallery),
      act("Actions", "Quick prompts", "saved prompts to insert", openQuickPrompts, "Ctrl P"),
      act("Actions", "Toggle quiet mode", "mute sound and notifications", () => {
        setQuiet(!quietOn());
        if (statusEl) statusEl.textContent = quietOn() ? "Quiet mode on" : "Quiet mode off";
      }),
    ];
  }

  // ----------------------------------------------------------- accessibility
  // help and settings are plain overlays in the page; make them behave as
  // modals: named dialog, focus inside, Tab trapped, background inert, and
  // focus back where it was when they close. Esc is answered in app.js.
  function modalize(id, label) {
    const el = $(id);
    if (!el) return;
    const card = el.querySelector(".help-card");
    if (card) { card.setAttribute("role", "dialog"); card.setAttribute("aria-modal", "true"); card.setAttribute("aria-label", label); }
    let back = null;
    const focusables = () => [...el.querySelectorAll('button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])')].filter((n) => !n.disabled && n.offsetParent !== null);
    new MutationObserver(() => {
      if (!el.hidden) {
        back = document.activeElement;
        if (shell) shell.inert = true;
        requestAnimationFrame(() => { const f = focusables(); if (f.length) f[0].focus(); });
      } else {
        if (shell) shell.inert = false;
        const b = back; back = null;
        if (b && b.isConnected && b !== document.body) b.focus(); else if (input) input.focus();
      }
    }).observe(el, { attributes: true, attributeFilter: ["hidden"] });
    el.addEventListener("keydown", (ev) => {
      if (ev.key !== "Tab") return;
      const f = focusables();
      if (!f.length) return;
      const first = f[0], last = f[f.length - 1];
      if (ev.shiftKey && (document.activeElement === first || !el.contains(document.activeElement))) { ev.preventDefault(); last.focus(); }
      else if (!ev.shiftKey && document.activeElement === last) { ev.preventDefault(); first.focus(); }
    });
  }
  modalize("help", "Help and keyboard shortcuts");
  modalize("settings", "Settings");

  // Keyboard resize: the two drag handles become separators the arrow keys move.
  function keyResize(id, label, min, max, dir, get, set, storageKey) {
    const h = $(id);
    if (!h) return;
    h.setAttribute("role", "separator");
    h.setAttribute("aria-orientation", "vertical");
    h.setAttribute("aria-label", label);
    h.tabIndex = 0;
    const sync = () => { h.setAttribute("aria-valuemin", min); h.setAttribute("aria-valuemax", max); h.setAttribute("aria-valuenow", Math.round(get())); };
    h.addEventListener("focus", sync);
    h.addEventListener("keydown", (ev) => {
      if (ev.key !== "ArrowLeft" && ev.key !== "ArrowRight") return;
      ev.preventDefault();
      const step = ev.shiftKey ? 48 : 16;
      const delta = (ev.key === "ArrowRight" ? 1 : -1) * step * dir;
      const w = Math.max(min, Math.min(max, Math.round(get() + delta)));
      set(w);
      store.set(storageKey, String(w));
      sync();
    });
  }
  const navPanel = document.querySelector(".nav"), workPanel = $("work");
  keyResize("nav-resize", "Resize the conversation list", 200, 440, 1,
    () => navPanel.getBoundingClientRect().width,
    (w) => document.documentElement.style.setProperty("--nav-w", w + "px"), "janetcode-nav-w");
  keyResize("work-resize", "Resize the session panel", 300, 520, -1,
    () => workPanel.getBoundingClientRect().width,
    (w) => { workPanel.style.width = w + "px"; shell.style.setProperty("--work-w-actual", w + "px"); }, "janetcode-work-w");

  // Workbench tabs: one tab stop, arrows move between them.
  (function rovingTabs() {
    const list = document.querySelector(".work-tabs");
    if (!list) return;
    const tabs = () => [...list.querySelectorAll('[role="tab"]')];
    function sync() {
      tabs().forEach((t) => {
        const on = t.classList.contains("on");
        t.setAttribute("aria-selected", on ? "true" : "false");
        t.tabIndex = on ? 0 : -1;
        const pane = document.querySelector('.work-pane[data-pane="' + t.dataset.pane + '"]');
        if (pane) { if (!pane.id) pane.id = "wp-" + t.dataset.pane; t.setAttribute("aria-controls", pane.id); }
      });
    }
    sync();
    new MutationObserver(sync).observe(list, { attributes: true, attributeFilter: ["class"], subtree: true });
    list.addEventListener("keydown", (ev) => {
      const t = tabs(), i = t.indexOf(document.activeElement);
      if (i < 0) return;
      let j = -1;
      if (ev.key === "ArrowRight") j = (i + 1) % t.length;
      else if (ev.key === "ArrowLeft") j = (i - 1 + t.length) % t.length;
      else if (ev.key === "Home") j = 0;
      else if (ev.key === "End") j = t.length - 1;
      if (j < 0) return;
      ev.preventDefault();
      t[j].click();
      t[j].focus();
    });
  })();

  window.JanetExtras = { toolDefault, uploaded, actions, openExport, openQuickPrompts, openGallery, openArtifact, quiet: quietOn };
  // Anything rendered before this file ran.
  decorateCode(); categorize(); markStopped();
})();
