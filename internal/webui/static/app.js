(() => {
  const log = document.getElementById("log");
  const empty = document.getElementById("empty");
  const input = document.getElementById("input");
  const form = document.getElementById("composer");
  const statusEl = document.getElementById("status");
  const ask = document.getElementById("ask");
  const askLine = document.getElementById("ask-line");
  const askPreview = document.getElementById("ask-preview");
  const palette = document.getElementById("palette");
  const paletteQ = document.getElementById("palette-q");
  const paletteList = document.getElementById("palette-list");
  const ac = document.getElementById("ac");
  let acItems = [];
  let acIdx = 0;
  let acStart = -1;
  let acTimer = 0;
  let curTitle = "";
  let lastPrompt = "";

  const nodes = new Map();
  const latest = new Map();
  const openTools = new Set();
  let running = false;
  let pending = null;
  let commands = [];
  let palIdx = 0;
  let palItems = [];
  let lastReply = "";

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }
  function md(src) {
    const fences = [];
    let t = String(src ?? "").replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
      fences.push(`<pre><code>${esc(code.replace(/\n$/, ""))}</code></pre>`);
      return `\u0000${fences.length - 1}\u0000`;
    });
    t = t.replace(/((?:^\|.+\|\s*$\n?)+)/gm, (block) => {
      const rows = block.trim().split("\n").map((r) =>
        r.trim().replace(/^\||\|$/g, "").split("|").map((c) => esc(c.trim())));
      if (rows.length < 2 || !/^[:\-\s|]+$/.test(block.trim().split("\n")[1].replace(/^\||\|$/g, ""))) {
        return block;
      }
      const head = rows[0];
      const body = rows.slice(2);
      let html = `<table><thead><tr>${head.map((c) => `<th>${c}</th>`).join("")}</tr></thead><tbody>`;
      html += body.map((r) => `<tr>${r.map((c) => `<td>${c}</td>`).join("")}</tr>`).join("");
      fences.push(html + `</tbody></table>`);
      return `\u0000${fences.length - 1}\u0000`;
    });
    t = esc(t);
    t = t.replace(/^### (.+)$/gm, "<h3>$1</h3>");
    t = t.replace(/^## (.+)$/gm, "<h2>$1</h2>");
    t = t.replace(/^# (.+)$/gm, "<h1>$1</h1>");
    t = t.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    t = t.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    t = t.replace(/`([^`]+)`/g, "<code>$1</code>");
    t = t.replace(/^\- (.+)$/gm, "<li>$1</li>");
    t = t.replace(/(<li>.*<\/li>\n?)+/g, (m) => `<ul>${m}</ul>`);
    t = t.replace(/\n{2,}/g, "</p><p>");
    t = `<p>${t}</p>`;
    return t.replace(/\u0000(\d+)\u0000/g, (_, i) => fences[Number(i)]);
  }
  function diffHTML(src) {
    return String(src ?? "").split("\n").map((l) => {
      if (l.startsWith("+") && !l.startsWith("+++")) return `<span class="add">${esc(l)}</span>`;
      if (l.startsWith("-") && !l.startsWith("---")) return `<span class="del">${esc(l)}</span>`;
      if (l.startsWith("@@")) return `<span class="hunk">${esc(l)}</span>`;
      return esc(l);
    }).join("\n");
  }
  function stick() {
    const el = document.querySelector(".thread");
    return el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }
  function follow(was) {
    if (!was) return;
    const el = document.querySelector(".thread");
    el.scrollTop = el.scrollHeight;
  }

  function linkCards(text) {
    const urls = [...new Set((String(text || "").match(/https?:\/\/[^\s)\]"'<>`]+/g) || []))]
      .filter((u) => { try { new URL(u); return true; } catch (_) { return false; } })
      .slice(0, 4);
    if (!urls.length) return "";
    return `<div class="link-cards">` + urls.map((u) => {
      let host = u;
      try { host = new URL(u).host; } catch (_) {}
      return `<button type="button" class="link-card" data-url="${esc(u)}" title="Preview ${esc(u)}">${esc(host)}</button>`;
    }).join("") + `</div>`;
  }

  function render(e) {
    latest.set(e.id, e);
    empty.hidden = true;
    let n = nodes.get(e.id);
    const was = stick();
    if (!n) {
      n = document.createElement("article");
      n.className = "turn";
      n.dataset.id = e.id;
      log.appendChild(n);
      nodes.set(e.id, n);
    }
    if (e.kind === "user") {
      n.className = "turn you";
      n.innerHTML = `<span class="lbl">You</span><div class="bubble">${esc(e.text || "")}</div>`;
    } else if (e.kind === "assistant") {
      n.className = "turn";
      const copyBtn = e.done ? `<button type="button" class="copy-btn" data-copy="${e.id}" title="Copy reply">Copy</button>` : "";
      n.innerHTML = `<span class="lbl">janetcode ${copyBtn}</span><div class="md">${md(e.text || "")}${e.done ? "" : "▍"}</div>${e.done ? linkCards(e.text) : ""}`;
      if (e.done) lastReply = (e.text || "").slice(0, 200);
    } else if (e.kind === "thinking") {
      n.className = "turn thought";
      const lines = (e.text || "").trim().split("\n");
      n.innerHTML = `<span class="lbl">Thinking</span>${esc(e.done ? (lines[0] || "").slice(0, 160) : lines.slice(-2).join(" "))}`;
    } else if (e.kind === "tool") {
      n.className = "turn";
      const st = e.denied ? "Declined" : e.error ? "Failed" : e.done ? "Done" : e.running ? "Running" : "Queued";
      const stCls = "st-" + st.toLowerCase();
      const open = openTools.has(e.id);
      let html = `<div class="tool ${stCls}"><div class="head"><span class="st">${st}</span><b>${esc(e.name || "")}</b><span class="sum">${esc(e.summary || "")}${e.dur ? " · " + esc(e.dur) : ""}</span></div>`;
      if (open && e.preview) html += `<pre class="diff">${diffHTML(e.preview)}</pre>`;
      if (open && e.output) html += `<pre class="out">${esc(e.output)}</pre>`;
      html += `</div>`;
      n.innerHTML = html;
      n.querySelector(".head").onclick = () => {
        if (openTools.has(e.id)) openTools.delete(e.id);
        else openTools.add(e.id);
        render(latest.get(e.id));
      };
    } else if (e.kind === "sheet") {
      n.className = "turn sheet";
      n.innerHTML = `<div class="k">${esc(e.name || "")}</div>${esc(e.text || "")}`;
    } else if (e.kind === "error") {
      n.className = "turn err";
      n.innerHTML = esc(e.text || "") + (lastPrompt ? ` <button type="button" class="retry-btn" data-retry="1">Retry</button>` : "");
    } else if (e.kind === "verdict") {
      n.className = "turn verdict" + ((e.text || "").startsWith("✓") ? " ok" : "");
      n.textContent = e.text || "";
    } else {
      n.className = "turn note";
      n.textContent = e.text || "";
    }
    follow(was);
  }

  function applyHello(h) {
    if (!h) return;
    document.getElementById("model").textContent = [h.provider, h.model].filter(Boolean).join(" · ");
    document.getElementById("cwd").textContent = h.cwd || "";
    if (h.title) curTitle = h.title;
    document.getElementById("title").textContent = curTitle || "New conversation";
    const leaf = (h.cwd || "").replace(/\\/g, "/").split("/").filter(Boolean).pop();
    if (leaf) document.getElementById("empty-lead").textContent = "What should we change in " + leaf + "?";
    document.title = (curTitle ? curTitle + " — " : "") + "janetcode";
    loadSessions();
  }

  function applyStatus(st) {
    if (!st) return;
    const prev = running;
    running = !!st.running;
    maybeNotify(prev, st);
    document.getElementById("send").textContent = running ? "Queue" : "Send";
    const bits = [];
    if (st.running) bits.push((st.phase || "Working") + (st.elapsed ? " · " + st.elapsed : ""));
    else if (st.status) bits.push(st.status);
    if (st.queued) bits.push(st.queued + " waiting");
    statusEl.textContent = bits.join(" · ");
    statusEl.classList.toggle("busy", !!st.running);
    refreshWorkspace();
  }

  function showAsk(p) {
    pending = p && p.name ? p : null;
    ask.hidden = !pending;
    if (!pending) return;
    askLine.textContent = "Allow " + p.name + (p.summary ? " — " + p.summary : "") + (p.warning ? "  Warning: " + p.warning : "") + "?";
    askPreview.hidden = !p.preview;
    if (p.preview) askPreview.innerHTML = diffHTML(p.preview);
  }

  function handle(msg) {
    switch (msg.type) {
      case "hello": applyHello(msg.hello); break;
      case "entry": if (msg.entry) render(msg.entry); break;
      case "status": applyStatus(msg.status); break;
      case "permission": showAsk(msg.permission); break;
      case "reset":
        log.innerHTML = "";
        nodes.clear();
        latest.clear();
        empty.hidden = false;
        curTitle = "";
        lastPrompt = "";
        break;
      case "notice":
        statusEl.textContent = msg.notice || "";
        break;
    }
  }

  async function post(path, body) {
    const r = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body || {})
    });
    if (!r.ok) statusEl.textContent = (await r.text()) || r.statusText;
    return r.ok;
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const text = input.value.trim();
    if (!text) return;
    if (!text.startsWith("/")) lastPrompt = text;
    input.value = "";
    resize();
    acHide();
    await post("/api/prompt", { text });
  });
  function acWord() {
    const pos = input.selectionStart;
    const before = input.value.slice(0, pos);
    const m = before.match(/(^|\s)@([^\s@]*)$/);
    if (!m) return null;
    return { start: pos - m[2].length, partial: m[2] };
  }
  function acHide() { ac.hidden = true; acItems = []; acStart = -1; }
  function acDraw() {
    ac.innerHTML = acItems.map((p, i) =>
      `<div class="${i === acIdx ? "on" : ""}" data-i="${i}">${esc(p)}</div>`
    ).join("") || `<div class="ac-hint">no matches</div>`;
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
    const sep = p.endsWith("/") ? "" : " ";
    input.value = input.value.slice(0, acStart) + p + sep + tail;
    input.selectionStart = input.selectionEnd = acStart + p.length + sep.length;
    acHide();
    resize();
    input.focus();
  }
  input.addEventListener("input", () => {
    resize();
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
  input.addEventListener("keydown", async (ev) => {
    if (!ac.hidden) {
      if (ev.key === "ArrowDown") { ev.preventDefault(); acIdx = Math.min(acIdx + 1, acItems.length - 1); acDraw(); return; }
      if (ev.key === "ArrowUp") { ev.preventDefault(); acIdx = Math.max(acIdx - 1, 0); acDraw(); return; }
      if (ev.key === "Enter" || ev.key === "Tab") { ev.preventDefault(); acAccept(); return; }
      if (ev.key === "Escape") { ev.preventDefault(); acHide(); return; }
    }
    if (ev.key === "Enter" && !ev.shiftKey) {
      ev.preventDefault();
      form.requestSubmit();
    }
    if (ev.key === "Escape") {
      ev.preventDefault();
      await post("/api/cancel", {});
    }
    if (pending && input.value === "" && !ev.metaKey && !ev.ctrlKey) {
      const map = { y: "allow", a: "session", r: "always", n: "deny", c: "cancel" };
      if (map[ev.key.toLowerCase()]) {
        ev.preventDefault();
        await post("/api/permission", { decision: map[ev.key.toLowerCase()] });
      }
    }
    if (ev.key === "s" && ev.ctrlKey && running) {
      ev.preventDefault();
      const text = input.value.trim();
      if (!text) return;
      input.value = "";
      await post("/api/steer", { text });
    }
  });

  ask.addEventListener("click", async (ev) => {
    const b = ev.target.closest("button[data-d]");
    if (!b) return;
    await post("/api/permission", { decision: b.dataset.d });
  });
  document.querySelector(".switches").addEventListener("click", async (ev) => {
    const b = ev.target.closest("button[data-cmd]");
    if (!b) return;
    await post("/api/command", { text: b.dataset.cmd });
  });

  function openPalette() {
    palette.hidden = false;
    paletteQ.value = "";
    palIdx = 0;
    drawPalette("");
    paletteQ.focus();
  }
  function closePalette() { palette.hidden = true; input.focus(); }
  function drawPalette(q) {
    q = q.toLowerCase().replace(/^\//, "");
    palItems = commands.filter((c) => {
      if (!q) return true;
      const n = c.name.slice(1);
      let i = 0;
      for (const ch of n) if (ch === q[i] && ++i === q.length) return true;
      return (c.help || "").toLowerCase().includes(q);
    }).slice(0, 10);
    paletteList.innerHTML = palItems.length
      ? palItems.map((c, i) =>
        `<div class="${i === palIdx ? "on" : ""}" data-i="${i}"><span class="name">${esc(c.name)}</span><span>${esc(c.help)}</span></div>`
      ).join("")
      : `<div class="empty-pal">No commands match</div>`;
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
      const c = palItems[palIdx];
      if (!c) return;
      closePalette();
      if (c.args) {
        input.value = c.name + " ";
        input.focus();
      } else {
        await post("/api/command", { text: c.name });
      }
    }
  });
  paletteList.addEventListener("click", async (ev) => {
    const row = ev.target.closest("[data-i]");
    if (!row) return;
    palIdx = Number(row.dataset.i);
    const c = palItems[palIdx];
    closePalette();
    if (c.args) { input.value = c.name + " "; input.focus(); }
    else await post("/api/command", { text: c.name });
  });

  async function refreshWorkspace() {
    try {
      const ws = await (await fetch("/api/workspace")).json();
      const todos = document.getElementById("todos");
      if (!ws.todos || !ws.todos.length) todos.innerHTML = `<li class="quiet">Nothing on the list yet</li>`;
      else todos.innerHTML = ws.todos.map((t) => {
        const cls = t.status === "completed" || t.status === "done" ? "done" : t.status === "in_progress" ? "doing" : "";
        return `<li class="${cls}">${esc(t.text)}</li>`;
      }).join("");
      const files = document.getElementById("files");
      if (!ws.files || !ws.files.length) files.innerHTML = `<li class="quiet">No files changed</li>`;
      else files.innerHTML = ws.files.map((f) =>
        `<li data-seq="${f.seq}"><span>${esc(f.path)}</span> <span class="add">+${f.add}</span> <span class="del">−${f.del}</span></li>`
      ).join("");
      document.getElementById("undo-btn").hidden = !ws.can_undo;
      const jobsSec = document.getElementById("jobs-sec");
      if (ws.jobs && ws.jobs.length) {
        jobsSec.hidden = false;
        document.getElementById("jobs").innerHTML = ws.jobs.map((j) => `<li>${esc(j.summary || j.command)}</li>`).join("");
      } else jobsSec.hidden = true;
      const agentsSec = document.getElementById("agents-sec");
      if (ws.agents && ws.agents.length) {
        agentsSec.hidden = false;
        document.getElementById("agents").innerHTML = ws.agents.map((a) =>
          `<li class="agent ${a.done ? "done" : "running"}"><span class="a-id">${esc(a.id)}</span> ${esc(a.desc || "")}` +
          (!a.done && a.last ? `<span class="a-last">${esc(a.last)}</span>` : "") + `</li>`
        ).join("");
      } else agentsSec.hidden = true;
      document.getElementById("ctx-line").textContent = ws.ctx_label ? "Context " + ws.ctx_label + " (" + ws.ctx_pct + "%)" : "Context —";
      document.getElementById("cost-line").textContent = ws.cost ? "Spend " + ws.cost : "";
      document.getElementById("mode-yolo").classList.toggle("on", !!ws.yolo);
      document.getElementById("mode-edits").classList.toggle("on", !!ws.edits);
      document.getElementById("mode-plan").classList.toggle("on", !!ws.plan);
      document.getElementById("mode-ro").classList.toggle("on", !!ws.readonly);
    } catch (_) {}
  }

  function relTime(iso) {
    const t = Date.parse((iso || "").replace(" ", "T"));
    if (!t) return "";
    const s = Math.max(0, (Date.now() - t) / 1000);
    if (s < 60) return "just now";
    if (s < 3600) return Math.floor(s / 60) + "m ago";
    if (s < 86400) return Math.floor(s / 3600) + "h ago";
    return Math.floor(s / 86400) + "d ago";
  }
  async function loadSessions() {
    try {
      const rows = await (await fetch("/api/sessions")).json();
      const ul = document.getElementById("sessions");
      if (!rows.length) { ul.innerHTML = `<li class="quiet">No other sessions</li>`; return; }
      ul.innerHTML = rows.slice(0, 40).map((r) =>
        `<li class="${r.current ? "on" : ""}" data-id="${esc(r.id)}" title="Resume this conversation"><span>${esc(r.label)}</span><span class="when">${esc(relTime(r.updated))}</span></li>`
      ).join("");
    } catch (_) {}
  }

  document.getElementById("sessions").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-id]");
    if (!li || li.classList.contains("on")) return;
    if (await post("/api/load", { id: li.dataset.id })) {
      log.innerHTML = "";
      nodes.clear();
      latest.clear();
      openTools.clear();
      empty.hidden = false;
      refreshWorkspace();
      loadSessions();
      input.focus();
    }
  });

  document.getElementById("files").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-seq]");
    if (!li) return;
    const data = await (await fetch("/api/change?n=" + li.dataset.seq)).json();
    document.getElementById("viewer-path").textContent = data.path || "";
    document.getElementById("viewer-diff").innerHTML = diffHTML(data.diff || "");
    document.getElementById("viewer").hidden = false;
  });
  document.getElementById("viewer-close").addEventListener("click", () => {
    document.getElementById("viewer").hidden = true;
  });
  document.getElementById("undo-btn").addEventListener("click", () => post("/api/command", { text: "/undo" }).then(refreshWorkspace));
  document.getElementById("new-chat").addEventListener("click", async () => {
    if (await post("/api/new", {})) {
      log.innerHTML = "";
      nodes.clear();
      latest.clear();
      empty.hidden = false;
      refreshWorkspace();
      loadSessions();
    }
  });

  fetch("/api/commands").then((r) => r.json()).then((c) => { commands = c || []; }).catch(() => {});
  fetch("/api/state").then((r) => r.json()).then((st) => {
    applyHello(st.hello);
    applyStatus(st.status);
    (st.entries || []).forEach(render);
    showAsk(st.permission);
    refreshWorkspace();
  }).catch(() => {});
  const themeBtn = document.getElementById("theme-toggle");
  function drawTheme() { themeBtn.textContent = document.documentElement.dataset.theme === "dark" ? "☀" : "☾"; }
  themeBtn.addEventListener("click", () => {
    const next = document.documentElement.dataset.theme === "dark" ? "light" : "dark";
    document.documentElement.dataset.theme = next;
    localStorage.setItem("janetcode-theme", next);
    drawTheme();
  });
  drawTheme();

  document.getElementById("empty").addEventListener("click", (ev) => {
    const chip = ev.target.closest(".chip");
    if (!chip) return;
    input.value = chip.dataset.q || "";
    resize();
    input.focus();
  });

  const shell = document.querySelector(".shell");
  const pvPane = document.getElementById("preview");
  const pvToggle = document.getElementById("preview-toggle");
  const pvUrl = document.getElementById("pv-url");
  const pvFrame = document.getElementById("pv-frame");
  const pvEmpty = document.getElementById("pv-empty");
  const pvStage = document.getElementById("pv-stage");

  function pvLoad(url) {
    url = String(url || "").trim();
    if (!url) return;
    if (!/^https?:\/\//i.test(url)) url = "http://" + url;
    pvUrl.value = url;
    pvFrame.src = url;
    pvFrame.hidden = false;
    pvEmpty.hidden = true;
    localStorage.setItem("janetcode-preview-url", url);
  }
  function pvOpen(open) {
    pvPane.hidden = !open;
    shell.classList.toggle("pv-open", open);
    pvToggle.classList.toggle("on", open);
    localStorage.setItem("janetcode-preview-open", open ? "1" : "");
    if (open && pvFrame.hidden && pvUrl.value) pvLoad(pvUrl.value);
  }
  pvToggle.addEventListener("click", () => pvOpen(pvPane.hidden));
  document.getElementById("pv-close").addEventListener("click", () => pvOpen(false));
  pvUrl.addEventListener("keydown", (ev) => { if (ev.key === "Enter") { ev.preventDefault(); pvLoad(pvUrl.value); } });
  document.getElementById("pv-refresh").addEventListener("click", () => { if (pvUrl.value) pvLoad(pvUrl.value); });
  document.getElementById("pv-ext").addEventListener("click", () => { if (pvUrl.value) window.open(pvUrl.value, "_blank", "noopener"); });
  const pvWidthBtn = document.getElementById("pv-width");
  pvWidthBtn.addEventListener("click", () => {
    const narrow = pvStage.classList.toggle("narrow");
    pvWidthBtn.classList.toggle("on", narrow);
    localStorage.setItem("janetcode-preview-narrow", narrow ? "1" : "");
  });
  log.addEventListener("click", async (ev) => {
    const copy = ev.target.closest(".copy-btn");
    if (copy) {
      const src = latest.get(copy.dataset.copy);
      if (src) {
        try {
          await navigator.clipboard.writeText(src.text || "");
          copy.textContent = "Copied";
          setTimeout(() => { copy.textContent = "Copy"; }, 1500);
        } catch (_) {}
      }
      return;
    }
    if (ev.target.closest(".retry-btn") && lastPrompt) {
      await post("/api/prompt", { text: lastPrompt });
      return;
    }
    const card = ev.target.closest(".link-card");
    if (!card) return;
    pvOpen(true);
    pvLoad(card.dataset.url);
  });
  pvUrl.value = localStorage.getItem("janetcode-preview-url") || "";
  if (localStorage.getItem("janetcode-preview-narrow")) { pvStage.classList.add("narrow"); pvWidthBtn.classList.add("on"); }
  if (localStorage.getItem("janetcode-preview-open")) pvOpen(true);

  async function uploadFiles(files) {
    if (!files.length) return;
    const fd = new FormData();
    files.forEach((f) => fd.append("files", f, f.name || "pasted-image.png"));
    statusEl.textContent = "Uploading " + files.length + " file" + (files.length > 1 ? "s" : "") + "…";
    try {
      const r = await fetch("/api/upload", { method: "POST", body: fd });
      const data = await r.json();
      if (!r.ok) { statusEl.textContent = typeof data === "string" ? data : r.statusText; return; }
      const refs = (data.paths || []).map((p) => "@" + p).join(" ");
      input.value = (input.value ? input.value.replace(/\s+$/, "") + " " : "") + refs + " ";
      resize();
      input.focus();
      statusEl.textContent = "Attached " + (data.paths || []).length + " file(s)";
    } catch (_) {
      statusEl.textContent = "Upload failed";
    }
  }
  input.addEventListener("paste", (ev) => {
    const files = [...(ev.clipboardData?.files || [])];
    if (!files.length) return;
    ev.preventDefault();
    uploadFiles(files);
  });

  const dropZone = document.querySelector(".col");
  let dragDepth = 0;
  dropZone.addEventListener("dragenter", (ev) => {
    if (![...(ev.dataTransfer?.types || [])].includes("Files")) return;
    ev.preventDefault();
    dragDepth++;
    form.classList.add("dragover");
  });
  dropZone.addEventListener("dragover", (ev) => {
    if ([...(ev.dataTransfer?.types || [])].includes("Files")) ev.preventDefault();
  });
  dropZone.addEventListener("dragleave", () => {
    if (--dragDepth <= 0) { dragDepth = 0; form.classList.remove("dragover"); }
  });
  dropZone.addEventListener("drop", async (ev) => {
    ev.preventDefault();
    dragDepth = 0;
    form.classList.remove("dragover");
    uploadFiles([...(ev.dataTransfer?.files || [])]);
  });

  const notifBtn = document.getElementById("notif-btn");
  const notifSupported = "Notification" in window;
  let notifyOn = notifSupported && localStorage.getItem("janetcode-notify") === "1" && Notification.permission === "granted";
  function drawNotif() {
    notifBtn.textContent = !notifSupported ? "Notifications unsupported"
      : notifyOn ? "Notifications on" : "Notifications off";
    notifBtn.classList.toggle("on", notifyOn);
  }
  notifBtn.addEventListener("click", async () => {
    if (!notifSupported) return;
    if (notifyOn) {
      notifyOn = false;
      localStorage.setItem("janetcode-notify", "");
    } else {
      const perm = await Notification.requestPermission();
      notifyOn = perm === "granted";
      localStorage.setItem("janetcode-notify", notifyOn ? "1" : "");
    }
    drawNotif();
  });
  drawNotif();
  function maybeNotify(prevRunning, st) {
    if (!notifyOn || !prevRunning || st.running || !document.hidden) return;
    const title = document.getElementById("title").textContent || "janetcode";
    const body = lastReply ? lastReply.split("\n")[0].slice(0, 120) : (st.status || "Turn finished");
    try { new Notification("janetcode · " + title, { body }); } catch (_) {}
  }

  const conn = document.getElementById("conn");
  const es = new EventSource("/events");
  es.onopen = () => { conn.classList.remove("off"); conn.textContent = "Live"; conn.title = "Live connection"; };
  es.onerror = () => { conn.classList.add("off"); conn.textContent = "Reconnecting"; conn.title = "Connection lost, retrying"; };
  es.onmessage = (ev) => { try { handle(JSON.parse(ev.data)); } catch (_) {} };
})();
