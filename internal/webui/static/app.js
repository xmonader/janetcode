(() => {
  const log = document.getElementById("log");
  const input = document.getElementById("input");
  const form = document.getElementById("composer");
  const noticeEl = document.getElementById("notice");
  const permEl = document.getElementById("perm");
  const permLine = document.getElementById("perm-line");
  const permPreview = document.getElementById("perm-preview");
  const permKeys = document.getElementById("perm-keys");
  const statusEl = document.getElementById("status");
  const modelEl = document.getElementById("model");
  const cwdEl = document.getElementById("cwd");
  const titleEl = document.getElementById("title");
  const costEl = document.getElementById("cost");
  const modesEl = document.getElementById("modes");
  const gaugeEl = document.getElementById("gauge");
  const sendBtn = document.getElementById("send");
  const drawer = document.getElementById("drawer");
  const tabsEl = document.getElementById("tabs");
  const drawerBody = document.getElementById("drawer-body");
  const panelBtn = document.getElementById("panel-btn");
  const menuEl = document.getElementById("menu");

  const nodes = new Map();
  const expanded = new Set();
  let running = false;
  let pending = null;
  let noticeTimer = 0;
  let commands = [];
  let menuIdx = 0;
  let verbose = false;
  let drawerTab = "context";

  const TABS = [
    ["context", "context"], ["stats", "stats"], ["todos", "todos"],
    ["jobs", "jobs"], ["changes", "changes"], ["memory", "memory"],
    ["skills", "skills"], ["sessions", "sessions"], ["inspect", "inspect"]
  ];

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  function md(src) {
    const fences = [];
    let t = String(src ?? "").replace(/```(\w*)\n([\s\S]*?)```/g, (_, lang, code) => {
      fences.push(`<pre><code class="${esc(lang)}">${esc(code.replace(/\n$/, ""))}</code></pre>`);
      return `\u0000${fences.length - 1}\u0000`;
    });
    t = esc(t);
    t = t.replace(/^### (.+)$/gm, "<h3>$1</h3>");
    t = t.replace(/^## (.+)$/gm, "<h2>$1</h2>");
    t = t.replace(/^# (.+)$/gm, "<h1>$1</h1>");
    t = t.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    t = t.replace(/`([^`]+)`/g, "<code>$1</code>");
    t = t.replace(/^\- (.+)$/gm, "<li>$1</li>");
    t = t.replace(/(<li>.*<\/li>\n?)+/g, (m) => `<ul>${m}</ul>`);
    t = t.replace(/\n{2,}/g, "</p><p>");
    t = `<p>${t}</p>`;
    t = t.replace(/\u0000(\d+)\u0000/g, (_, i) => fences[Number(i)]);
    return t;
  }

  function diffHTML(src) {
    return String(src ?? "").split("\n").map((l) => {
      if (l.startsWith("+") && !l.startsWith("+++")) return `<span class="add">${esc(l)}</span>`;
      if (l.startsWith("-") && !l.startsWith("---")) return `<span class="del">${esc(l)}</span>`;
      if (l.startsWith("@@")) return `<span class="hunk">${esc(l)}</span>`;
      return esc(l);
    }).join("\n");
  }

  function clip(text, n) {
    const lines = String(text ?? "").split("\n");
    if (verbose || expanded.has(n) || lines.length <= 12) return { html: esc(text), more: false };
    const tail = lines.slice(-8).join("\n");
    return { html: esc(`… ${lines.length - 8} earlier lines\n` + tail), more: true };
  }

  function pin() {
    return log.scrollHeight - log.scrollTop - log.clientHeight < 48;
  }
  function follow(was) {
    if (was) log.scrollTop = log.scrollHeight;
  }

  function renderEntry(e) {
    let n = nodes.get(e.id);
    const was = pin();
    if (!n) {
      n = document.createElement("article");
      n.className = "entry";
      n.dataset.id = e.id;
      log.appendChild(n);
      nodes.set(e.id, n);
      n.addEventListener("click", () => {
        if (e.kind === "tool" && (e.output || e.preview)) {
          if (expanded.has(e.id)) expanded.delete(e.id);
          else expanded.add(e.id);
          renderEntry(nodes.get(e.id) && e);
        }
      });
    }
    n.className = "entry " + e.kind + (e.error ? " error" : "") + (e.kind === "verdict" && (e.text || "").startsWith("✓") ? " ok" : "");
    if (e.kind === "assistant") {
      n.innerHTML = `<div class="md">${md(e.text || "")}${e.done ? "" : "<span class='dim'> ▍</span>"}</div>`;
    } else if (e.kind === "thinking") {
      const lines = (e.text || "").trim().split("\n");
      n.textContent = e.done
        ? "· thought: " + (lines[0] || "").slice(0, 100)
        : "· " + lines.slice(-3).join("\n");
    } else if (e.kind === "tool") {
      const icon = e.denied ? "⊘" : e.error ? "✗" : e.done ? "✓" : e.running ? "▶" : "⚙";
      let html = `${icon} <strong>${esc(e.name || "")}</strong> ${esc(e.summary || "")}`;
      if (e.dur) html += `<span class="meta">${esc(e.dur)}</span>`;
      const open = verbose || expanded.has(e.id);
      if (e.preview && open) html += `<pre class="diff">${diffHTML(e.preview)}</pre>`;
      else if (e.preview) html += `<div class="body dim">diff · click to expand</div>`;
      if (e.output) {
        const c = clip(e.output, e.id);
        html += `<div class="body${e.error ? " err" : ""}">${c.html}${c.more ? "\nclick to expand" : ""}</div>`;
      }
      n.innerHTML = html;
    } else if (e.kind === "sheet") {
      n.innerHTML = `<div class="name">${esc(e.name || "report")}</div><pre>${esc(e.text || "")}</pre>`;
    } else if (e.kind === "user") {
      n.textContent = e.text || "";
    } else {
      n.textContent = e.text || "";
    }
    follow(was);
  }

  function showNotice(text) {
    noticeEl.hidden = !text;
    noticeEl.textContent = text || "";
    clearTimeout(noticeTimer);
    if (text) {
      noticeTimer = setTimeout(() => { noticeEl.hidden = true; noticeEl.textContent = ""; }, 3000);
    }
  }

  function showPerm(p) {
    pending = p && p.name ? p : null;
    if (!pending) {
      permEl.hidden = true;
      form.classList.toggle("warn", false);
      return;
    }
    permEl.hidden = false;
    form.classList.add("warn");
    let line = `allow ${p.name} ${p.summary || ""}`;
    if (p.warning) line += ` · ⚠ ${p.warning}`;
    permLine.textContent = line;
    permPreview.hidden = !p.preview;
    if (p.preview) permPreview.innerHTML = diffHTML(p.preview);
    permKeys.innerHTML = `<kbd>y</kbd> once · <kbd>a</kbd> always for ${esc(p.name)} · <kbd>r</kbd> rule ${esc(p.rule || "")} · <kbd>n</kbd> deny · <kbd>c</kbd> cancel`;
    input.placeholder = "y allow once · a always · r save rule · n deny · c cancel";
  }

  function setModes(st) {
    const bits = [
      ["YOLO", st.yolo], ["EDITS", st.edits], ["PLAN", st.plan], ["READ-ONLY", st.readonly]
    ];
    modesEl.innerHTML = bits.filter(([, on]) => on).map(([n]) => `<span class="mode on">${n}</span>`).join("");
  }

  function applyStatus(st) {
    if (!st) return;
    running = !!st.running;
    verbose = !!st.verbose;
    form.classList.toggle("busy", running && !pending);
    const bits = [];
    if (st.running) bits.push((st.phase || "working") + (st.elapsed ? " · " + st.elapsed : "") + " · esc to cancel");
    else bits.push(st.status || "ready");
    if (st.queued) bits.push("queued " + st.queued);
    statusEl.textContent = bits.join(" · ");
    const pct = Math.max(0, Math.min(100, st.ctx_pct || 0));
    const bar = gaugeEl.querySelector("i");
    if (bar) bar.style.setProperty("--pct", pct + "%");
    sendBtn.textContent = running ? "queue" : "send";
    costEl.textContent = st.cost || "";
    setModes(st);
  }

  function applyHello(h) {
    if (!h) return;
    modelEl.textContent = [h.provider, h.model].filter(Boolean).join("/");
    cwdEl.textContent = h.cwd || "";
    titleEl.textContent = h.title || "";
    document.title = (h.title ? h.title + " · " : "") + "janetcode";
    setModes(h);
  }

  function handle(msg) {
    switch (msg.type) {
      case "hello": applyHello(msg.hello); break;
      case "entry": if (msg.entry) renderEntry(msg.entry); break;
      case "status": applyStatus(msg.status); break;
      case "permission": showPerm(msg.permission); break;
      case "notice": showNotice(msg.notice); break;
      case "error": showNotice(msg.error); break;
    }
  }

  async function post(path, body) {
    const r = await fetch(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body || {})
    });
    if (!r.ok) showNotice((await r.text()) || r.statusText);
    return r.ok;
  }

  function matches(cmd, q) {
    q = q.toLowerCase().replace(/^\//, "");
    const n = cmd.name.toLowerCase().slice(1);
    if (!q) return true;
    let i = 0;
    for (const ch of n) {
      if (ch === q[i]) i++;
      if (i === q.length) return true;
    }
    return (cmd.help || "").toLowerCase().includes(q);
  }

  function menuItems() {
    const v = input.value;
    if (!v.startsWith("/")) return [];
    const q = v.slice(1).split(/\s/)[0];
    return commands.filter((c) => matches(c, q)).slice(0, 12);
  }

  function drawMenu() {
    const items = menuItems();
    if (!items.length || (input.value.startsWith("/") && input.value.includes(" ") && !input.value.endsWith(" "))) {
      menuEl.hidden = true;
      return;
    }
    if (menuIdx >= items.length) menuIdx = items.length - 1;
    if (menuIdx < 0) menuIdx = 0;
    menuEl.hidden = false;
    menuEl.innerHTML = items.map((c, i) =>
      `<div class="row${i === menuIdx ? " on" : ""}"><span class="name">${esc(c.name)}</span><span>${esc(c.args || "")}</span><span>${esc(c.help)}</span></div>`
    ).join("");
  }

  function acceptMenu() {
    const items = menuItems();
    if (!items.length) return false;
    const c = items[menuIdx] || items[0];
    input.value = c.name + (c.args ? " " : "");
    menuEl.hidden = true;
    resize();
    return true;
  }

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    const text = input.value.trim();
    if (!text) return;
    input.value = "";
    menuEl.hidden = true;
    resize();
    await post("/api/prompt", { text });
  });

  function resize() {
    input.style.height = "auto";
    input.style.height = Math.min(input.scrollHeight, 144) + "px";
  }
  input.addEventListener("input", () => { resize(); drawMenu(); });

  input.addEventListener("keydown", async (ev) => {
    if (!menuEl.hidden && (ev.key === "ArrowDown" || ev.key === "ArrowUp" || ev.key === "Tab")) {
      ev.preventDefault();
      if (ev.key === "Tab") { acceptMenu(); return; }
      const n = menuItems().length;
      menuIdx = (menuIdx + (ev.key === "ArrowDown" ? 1 : n - 1)) % n;
      drawMenu();
      return;
    }
    if (ev.key === "Enter" && !ev.shiftKey) {
      ev.preventDefault();
      if (!menuEl.hidden) { acceptMenu(); return; }
      form.requestSubmit();
      return;
    }
    if (ev.key === "Escape") {
      ev.preventDefault();
      if (!menuEl.hidden) { menuEl.hidden = true; return; }
      await post("/api/cancel", {});
      return;
    }
    if (ev.key === "]" && ev.target !== input) {
      ev.preventDefault();
      toggleDrawer();
      return;
    }
    if (pending && !ev.metaKey && !ev.ctrlKey && !ev.altKey) {
      const map = { y: "allow", a: "session", r: "always", n: "deny", c: "cancel" };
      const k = ev.key.toLowerCase();
      if (map[k] && ev.target === input && input.value === "") {
        ev.preventDefault();
        await post("/api/permission", { decision: map[k] });
      }
    }
    if (ev.key === "s" && ev.ctrlKey && running) {
      ev.preventDefault();
      const text = input.value.trim();
      if (!text) return;
      input.value = "";
      resize();
      await post("/api/steer", { text });
    }
  });

  document.addEventListener("keydown", (ev) => {
    if (ev.key === "]" && ev.target !== input && ev.target.tagName !== "TEXTAREA") {
      ev.preventDefault();
      toggleDrawer();
    }
  });

  function toggleDrawer() {
    drawer.hidden = !drawer.hidden;
    panelBtn.classList.toggle("open", !drawer.hidden);
    if (!drawer.hidden) loadPanel(drawerTab);
  }
  panelBtn.addEventListener("click", toggleDrawer);

  tabsEl.innerHTML = TABS.map(([id, label]) => `<button type="button" data-tab="${id}">${label}</button>`).join("");
  tabsEl.addEventListener("click", (ev) => {
    const b = ev.target.closest("button");
    if (!b) return;
    drawerTab = b.dataset.tab;
    [...tabsEl.children].forEach((x) => x.classList.toggle("on", x === b));
    loadPanel(drawerTab);
  });
  tabsEl.querySelector("button").classList.add("on");

  async function loadPanel(name) {
    drawerBody.textContent = "…";
    try {
      if (name === "sessions") {
        const rows = await (await fetch("/api/sessions")).json();
        drawerBody.textContent = rows.map((r) =>
          `${r.current ? "▸" : " "} ${r.id}  ${r.label}  (${r.messages} msgs)`
        ).join("\n") || "no sessions in this directory";
        return;
      }
      if (name === "inspect") {
        const rows = await (await fetch("/api/inspect")).json();
        drawerBody.textContent = (rows || []).map((r) => `${r.name.padEnd(16)} ${r.summary}`).join("\n")
          + "\n\n/inspect <name> prints one into the transcript";
        return;
      }
      const p = await (await fetch("/api/panel?name=" + encodeURIComponent(name))).json();
      drawerBody.textContent = p.body || "";
    } catch (err) {
      drawerBody.textContent = String(err);
    }
  }

  fetch("/api/commands").then((r) => r.json()).then((c) => { commands = c || []; }).catch(() => {});
  fetch("/api/state").then((r) => r.json()).then((st) => {
    applyHello(st.hello);
    applyStatus(st.status);
    (st.entries || []).forEach(renderEntry);
    showPerm(st.permission);
  }).catch(() => {});

  const es = new EventSource("/events");
  es.onmessage = (ev) => {
    try { handle(JSON.parse(ev.data)); } catch (_) {}
  };
})();
