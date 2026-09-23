(() => {
  // Inline SVG from the sprite in index.html — the only icon source.
  const icon = (name) => `<svg class="i" aria-hidden="true"><use href="#i-${name}"/></svg>`;
  const log = document.getElementById("log");
  const empty = document.getElementById("empty");
  const input = document.getElementById("input");
  const form = document.getElementById("composer");
  const statusEl = document.getElementById("status");
  const ask = document.getElementById("ask");
  const askLine = document.getElementById("ask-line");
  const askRule = document.getElementById("ask-rule");
  const askRungs = document.getElementById("ask-rungs");
  const askRung = document.getElementById("ask-rung");
  const askImpact = document.getElementById("ask-impact");
  const askPreview = document.getElementById("ask-preview");
  const questionsEl = document.getElementById("questions");
  const qPrompt = document.getElementById("q-prompt");
  const qDetail = document.getElementById("q-detail");
  const qOpts = document.getElementById("q-opts");
  const qOther = document.getElementById("q-other");
  const qOtherText = document.getElementById("q-other-text");
  let pendingQs = [];
  let qIdx = 0;
  let qAnswers = [];
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
  const openThoughts = new Set();
  const openAnswers = new Set();
  const openRuns = new Set();
  const openFileCards = new Set();
  let selectedAgent = "";
  let running = false;
  let pending = null;
  let commands = [];
  let palIdx = 0;
  let palItems = [];
  let lastReply = "";
  let lastUserText = "";
  let lastFlavor = "";

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  // highlight is a deliberately small tokenizer: comments, strings,
  // numbers and keywords, colored generically so one pass serves every
  // language the model writes. Everything it emits is escaped first; the
  // spans are markup, never source.



  function changedSpan(line, other) {
    if (line === other) return [0, 0];
    const a = [...line], b = [...other];
    let indent = 0;
    while (indent < a.length && (a[indent] === " " || a[indent] === "\t")) indent++;
    let p = indent;
    while (p < a.length && p < b.length && a[p] === b[p]) p++;
    let s = 0;
    while (s < a.length - p && s < b.length - p && a[a.length - 1 - s] === b[b.length - 1 - s]) s++;
    const word = (ch) => /[A-Za-z0-9_]/.test(ch) || (ch && ch.codePointAt(0) > 127);
    while (p > indent && word(a[p - 1])) p--;
    while (s > 0 && s < a.length - p && word(a[a.length - s])) s--;
    if (p >= a.length - s) return [0, 0];
    return [p, a.length - s];
  }
  function markSpan(text, other, cls) {
    const [from, to] = changedSpan(text, other);
    if (from >= to) return esc(text);
    const a = [...text];
    return esc(a.slice(0, from).join("")) + `<span class="mark">${esc(a.slice(from, to).join(""))}</span>` + esc(a.slice(to).join(""));
  }
  function diffHTML(src) {
    const lines = String(src ?? "").split("\n");
    if (!lines.some((l) => l.startsWith("@@"))) {
      return lines.map((l) => {
        if (l.startsWith("+") && !l.startsWith("+++")) return `<span class="add">${esc(l)}</span>`;
        if (l.startsWith("-") && !l.startsWith("---")) return `<span class="del">${esc(l)}</span>`;
        return esc(l);
      }).join("\n");
    }
    const out = [];
    let newN = 1, oldN = 1, prevEnd = 0, hunkStart = false;
    const gw = String(Math.max(lines.length, 1)).length;
    for (let i = 0; i < lines.length; i++) {
      const l = lines[i];
      const h = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/.exec(l);
      if (h) {
        oldN = Number(h[1]);
        newN = Number(h[2]);
        if (hunkStart && newN - prevEnd > 0) {
          out.push(`<span class="gap">${esc(" ".repeat(gw + 2) + "… " + (newN - prevEnd) + " unchanged lines")}</span>`);
        }
        hunkStart = true;
        continue;
      }
      if (l.startsWith("+++") || l.startsWith("---") || l.startsWith("diff ") || l.startsWith("index ")) continue;
      const kind = l[0];
      const text = l.slice(1);
      let n = newN;
      let cls = "";
      if (kind === "+") { cls = "add"; n = newN; newN++; }
      else if (kind === "-") { cls = "del"; n = oldN; oldN++; }
      else if (kind === " " || kind === undefined) { n = newN; oldN++; newN++; }
      else { out.push(esc(l)); continue; }
      let body = esc(text);
      if (kind === "-" && lines[i + 1] && lines[i + 1][0] === "+" && lines[i + 2] && lines[i + 2][0] !== "+" && (i === 0 || lines[i - 1][0] !== "-")) {
        body = markSpan(text, lines[i + 1].slice(1));
      } else if (kind === "+" && i > 0 && lines[i - 1][0] === "-" && (!lines[i + 1] || lines[i + 1][0] !== "+")) {
        body = markSpan(text, lines[i - 1].slice(1));
      }
      const gutter = String(n).padStart(gw, " ");
      out.push(`<span class="${cls}"><span class="ln">${esc(gutter)} │ </span>${kind === "+" || kind === "-" ? kind : " "}${body}</span>`);
      if (kind !== "-") prevEnd = newN;
    }
    return out.join("\n");
  }
  // scroller is what actually scrolls: the chat column. (.thread is
  // overflow:hidden — it sized the column once and stick/follow used to
  // set scrollTop on it, which scrolls nothing.)
  function scroller() {
    return document.getElementById("chat-view");
  }
  function stick() {
    const el = scroller();
    return el.scrollHeight - el.scrollTop - el.clientHeight < 80;
  }
  function follow(was) {
    if (!was) return;
    const el = scroller();
    el.scrollTop = el.scrollHeight;
  }

  // perfCardHTML and usageCardHTML draw the structured companions of
  // /perf and /usage: the same numbers the text sheets print, as a
  // stacked wall-clock bar and per-model rows.
  function perfCardHTML(d) {
    if (!d) return "";
    const sliceCls = { model: "sl-model", tools: "sl-tools", you: "sl-you", harness: "sl-harness" };
    const total = Math.max(d.open_ms, 1);
    const bar = (d.slices || [])
      .filter((s) => s.ms > 0)
      .map((s) => `<span class="${sliceCls[s.name] || "sl-harness"}" style="flex:${Math.max(s.ms, 1)}" title="${esc(s.name)} ${esc(fmtDur(s.ms))}"></span>`)
      .join("");
    const rows = (d.slices || [])
      .map((s) => {
        const bits = [];
        if (s.count) bits.push(`${s.count}×`);
        bits.push(`${s.pct}% of the session`);
        if (s.slowest_ms) bits.push(`slowest ${fmtDur(s.slowest_ms)}`);
        return `<div class="crow"><span class="cbar ${sliceCls[s.name] || "sl-harness"}"></span>` +
          `<b>${esc(s.name)}</b><span class="cms">${esc(fmtDur(s.ms))}</span>` +
          `<span class="cnote">${esc(bits.join(" · "))}${s.note ? " — " + esc(s.note) : ""}</span></div>`;
      })
      .join("");
    return (
      `<div class="k">where the wall clock went</div>` +
      `<div class="perfbar">${bar || `<span class="sl-harness" style="flex:1"></span>`}</div>` +
      `<div class="perfmeta">open ${esc(fmtDur(d.open_ms))} · in turns ${esc(fmtDur(d.turn_ms))} (${Math.round((d.turn_ms / total) * 100)}%) · outside them ${esc(fmtDur(d.idle_ms))}</div>` +
      rows
    );
  }

  function usageCardHTML(d) {
    if (!d) return "";
    const maxTok = Math.max(1, ...(d.models || []).map((m) => m.in + m.cache_read + m.cache_write + m.out));
    const rows = (d.models || [])
      .map((m) => {
        const all = m.in + m.cache_read + m.cache_write + m.out;
        return `<div class="crow"><span class="cbar sl-model" style="width:${Math.max((all / maxTok) * 100, 2)}%"></span>` +
          `<b>${esc(m.model)}</b><span class="cms">${m.calls} call${m.calls === 1 ? "" : "s"}</span>` +
          `<span class="cnote">↑${fmtTok(m.in)} (⧉${fmtTok(m.cache_read + m.cache_write)}) ↓${fmtTok(m.out)}` +
          (m.cost_usd ? ` · $${m.cost_usd.toFixed(4)}` : "") + `</span></div>`;
      })
      .join("");
    const foot = [];
    if (d.in + d.out) foot.push(`↑${fmtTok(d.in)} ↓${fmtTok(d.out)}${d.cache ? " · ⧉" + fmtTok(d.cache) : ""}`);
    if (d.cache_pct) foot.push(`cache hit ${d.cache_pct}%`);
    if (d.cost_usd) foot.push(`$${d.cost_usd.toFixed(4)} reported`);
    else foot.push("unpriced");
    if (d.ceiling) foot.push(`ceiling $${d.ceiling.toFixed(2)}`);
    return `<div class="k">what the session spent</div>` + rows + `<div class="perfmeta">${esc(foot.join(" · "))}</div>`;
  }

  // outputHTML renders a tool result's body: highlighted when the tool
  // was reading a file whose language we know, plain otherwise. A long
  // output stays plain — the tokenizer is fast, but not on a megabyte.



  function linkCards(text) {    const urls = [...new Set((String(text || "").match(/https?:\/\/[^\s)\]"'<>`]+/g) || []))]
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
    if (e.at) n.title = new Date(e.at).toLocaleString();
    if (e.kind === "user") {
      n.className = "turn you";
      lastUserText = e.text || "";
      if (!curTitle) paintTitle();
      loadSessions();
      n.innerHTML = `<div class="bubble">${esc(e.text || "")}</div><div class="msg-actions">` +
        `<button type="button" class="act copy-btn" data-copy="${e.id}" title="Copy" aria-label="Copy message">${icon("copy")}</button>` +
        `<button type="button" class="act" data-edit="${e.id}" title="Edit & resend" aria-label="Edit and resend">${icon("pencil")}</button></div>`;
    } else if (e.kind === "assistant") {
      n.className = "turn";
      const copyBtn = `<button type="button" class="act copy-btn" data-copy="${e.id}" title="Copy reply" aria-label="Copy reply">${icon("copy")}</button>`;
      const regenBtn = lastUserText ? `<button type="button" class="act" data-regen="1" title="Ask again" aria-label="Ask again">${icon("refresh")}</button>` : "";
      const meta = [];
      if (e.at) meta.push(new Date(e.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }));
      if (e.tok_in || e.tok_out) meta.push("↑" + fmtTok(e.tok_in) + " ↓" + fmtTok(e.tok_out));
      if (e.ms) meta.push(fmtDur(e.ms));
      const metaRow = e.done ? `<div class="msg-foot"><span class="msg-actions">${copyBtn}${regenBtn}</span><span class="msg-meta">${esc(meta.join(" · "))}</span></div>` : "";
      const long = e.done && (e.text || "").length > 1800;
      const clipped = long && !openAnswers.has(e.id);
      const more = clipped ? `<button type="button" class="continue" data-more="${e.id}">Continue</button>` : "";
      n.innerHTML = `<div class="md${clipped ? " clip" : ""}">${JanetMD.render(e.text || "", e.done)}${e.done ? "" : "▍"}</div>${more}${metaRow}${e.done ? linkCards(e.text) : ""}`;
      if (e.done) {
        lastReply = (e.text || "").slice(0, 200);
        drawMermaid(n);
      }
    } else if (e.kind === "thinking") {
      n.className = "turn thought";
      n.dataset.thought = e.id;
      const raw = (e.text || "").trim();
      if (!raw && !e.done) {
        n.innerHTML = `<span class="lbl">${esc(lastFlavor || "Thinking")}</span>`;
      } else if (e.done && !openThoughts.has(e.id)) {
        const line = (raw.split("\n")[0] || "").slice(0, 160);
        n.innerHTML = `<span class="lbl">Thought <span class="chev">${icon("chevron-right")}</span></span>${esc(line)}`;
      } else if (e.done) {
        n.innerHTML = `<span class="lbl">Thought <span class="chev">${icon("chevron-down")}</span></span><pre class="thought-body">${esc(raw)}</pre>`;
      } else {
        const lines = raw.split("\n");
        n.innerHTML = `<span class="lbl">Thinking</span>${esc(lines.slice(-2).join(" "))}`;
      }
    } else if (e.kind === "tool") {
      const folded = !!(e.run && !e.run_live && !openRuns.has(e.run));
      n.className = "turn" + (e.run && !e.run_head ? " run-child" : "");
      n.hidden = !!(e.run && !e.run_head && folded);
      const st = e.denied ? "Declined" : e.error ? "Failed" : e.done ? "Done" : e.running ? "Running" : "Queued";
      const stCls = "st-" + st.toLowerCase();
      const open = openTools.has(e.id);
      let bar = "";
      if (e.run_head) {
        bar = `<button type="button" class="run-bar${/failed/.test(e.run_label || "") ? " bad" : ""}" data-run="${esc(e.run)}"><span class="chev">${icon(folded ? "chevron-right" : "chevron-down")}</span><span>${esc(e.run_label || "")}</span></button>`;
      }
      const bodyHidden = e.run_head && folded;
      let html = bar + `<div class="tool ${stCls}"${bodyHidden ? " hidden" : ""}><div class="head"><span class="st">${st}</span><b>${esc(e.name || "")}</b><span class="sum">${esc(e.summary || "")}${e.dur ? " · " + esc(e.dur) : ""}</span></div>`;
      if (open && e.preview) html += `<pre class="diff">${diffHTML(e.preview)}</pre>`;
      if (open && e.output) html += `<pre class="out">${JanetMD.renderOutput(e.name || "", e.summary || "", e.output)}</pre>`;
      html += `</div>`;
      n.innerHTML = html;
      const head = n.querySelector(".tool .head");
      if (head) head.onclick = () => {
        if (openTools.has(e.id)) openTools.delete(e.id);
        else openTools.add(e.id);
        render(latest.get(e.id));
      };
    } else if (e.kind === "perf") {
      n.className = "turn card";
      n.innerHTML = perfCardHTML(e.data);
    } else if (e.kind === "usage") {
      n.className = "turn card";
      n.innerHTML = usageCardHTML(e.data);
    } else if (e.kind === "sheet") {
      n.className = "turn sheet";
      n.innerHTML = `<div class="k">${esc(e.name || "")}<button type="button" class="act copy-btn sheet-copy" data-copy="${e.id}" title="Copy output" aria-label="Copy output">${icon("copy")}</button></div><pre class="sheetbody">${esc(e.text || "")}</pre>`;
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
    paintToLatest();
  }

  async function drawMermaid(root) {
    for (const block of root.querySelectorAll('.codeblock[data-lang="mermaid"]')) {
      if (block.dataset.drawn) continue;
      const bid = block.querySelector("[data-bid]")?.dataset.bid;
      const src = JanetMD.codeBlocks.get(bid);
      if (!src) continue;
      block.dataset.drawn = "1";
      try {
        const res = await fetch("/api/mermaid", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ src }),
        });
        const data = await res.json();
        if (!data.ok || !data.lines || !data.lines.length) continue;
        const pre = block.querySelector("pre");
        const diagram = data.lines.join("\n");
        pre.className = "diagram";
        pre.textContent = diagram;
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "codecopy";
        btn.textContent = "Source";
        btn.addEventListener("click", () => {
          if (pre.classList.contains("diagram")) {
            pre.className = "";
            pre.innerHTML = `<code>${JanetMD.highlight(src, "")}</code>`;
            btn.textContent = "Diagram";
          } else {
            pre.className = "diagram";
            pre.textContent = diagram;
            btn.textContent = "Source";
          }
        });
        block.querySelector(".codehead").appendChild(btn);
      } catch (_) {}
    }
  }

  log.addEventListener("click", async (ev) => {
    const wrap = ev.target.closest("[data-wrap]");
    if (wrap) {
      const pre = wrap.closest(".codeblock")?.querySelector("pre");
      if (pre) pre.style.whiteSpace = pre.style.whiteSpace === "pre-wrap" ? "pre" : "pre-wrap";
      return;
    }
    const more = ev.target.closest("[data-more]");
    if (more) {
      openAnswers.add(more.dataset.more);
      render(latest.get(more.dataset.more));
      return;
    }
    const runBtn = ev.target.closest("[data-run]");
    if (runBtn) {
      const id = runBtn.dataset.run;
      if (openRuns.has(id)) openRuns.delete(id); else openRuns.add(id);
      latest.forEach((e) => { if (e.run === id) render(e); });
      return;
    }
    const thought = ev.target.closest("[data-thought]");
    if (thought && latest.get(thought.dataset.thought)?.done) {
      const id = thought.dataset.thought;
      if (openThoughts.has(id)) openThoughts.delete(id); else openThoughts.add(id);
      render(latest.get(id));
      return;
    }
    const filesMore = ev.target.closest("[data-files]");
    if (filesMore) {
      const t = filesMore.dataset.files;
      if (openFileCards.has(t)) openFileCards.delete(t); else openFileCards.add(t);
      if (lastWorkspace) paintTurnFiles(lastWorkspace.files);
      return;
    }
    const fileLi = ev.target.closest(".turn-files li[data-seq]");
    if (fileLi) {
      const data = await (await fetch("/api/change?n=" + fileLi.dataset.seq)).json();
      showDetail(data.path || "", data.diff || "", true);
    }
  });

  let lastWorkspace = null;
  function paintTurnFiles(files) {
    log.querySelectorAll(".turn-files").forEach((n) => n.remove());
    if (!files || !files.length) return;
    const byTurn = new Map();
    for (const f of files) {
      const t = f.turn || 0;
      if (!byTurn.has(t)) byTurn.set(t, []);
      byTurn.get(t).push(f);
    }
    const users = [...log.querySelectorAll("article.turn.you")];
    for (const [turn, list] of byTurn) {
      const card = document.createElement("div");
      card.innerHTML = fileCardHTML(turn, list);
      const node = card.firstElementChild;
      const anchor = users[turn - 1];
      if (!anchor) { log.appendChild(node); continue; }
      let after = anchor;
      let n = anchor.nextElementSibling;
      const nextUser = users[turn] || null;
      while (n && n !== nextUser) { after = n; n = n.nextElementSibling; }
      after.insertAdjacentElement("afterend", node);
    }
  }
  function fileCardHTML(turn, list) {
    const open = openFileCards.has(String(turn));
    const shown = open ? list : list.slice(0, 3);
    let add = 0, del = 0;
    for (const f of list) { add += f.add || 0; del += f.del || 0; }
    const rows = shown.map((f) =>
      `<li data-seq="${f.seq}"><span>${esc(f.path)}</span><span><span class="add">+${f.add || 0}</span> <span class="del">−${f.del || 0}</span></span></li>`
    ).join("");
    const rest = !open && list.length > 3 ? `<li class="more" data-files="${turn}">${list.length - 3} more files</li>` : "";
    return `<div class="turn-files" data-turn-files="${turn}"><header><span>${list.length} file${list.length === 1 ? "" : "s"} changed</span><span><span class="add">+${add}</span> <span class="del">−${del}</span></span></header>${rows}${rest}</div>`;
  }
  function paintGit(ws) {
    const el = document.getElementById("git-pill");
    if (!el) return;
    const files = (ws && ws.files) || [];
    let add = 0, del = 0;
    for (const f of files) { add += f.add || 0; del += f.del || 0; }
    if (!ws || (!ws.branch && !add && !del)) { el.hidden = true; return; }
    el.hidden = false;
    el.innerHTML = `${esc(ws.branch || "git")}` + (add || del ? ` <span class="add">+${add}</span> <span class="del">−${del}</span>` : "");
  }

  let modelLabel = "";
  function paintModel() {
    const effort = document.getElementById("effort-chip")?.dataset.effort || "";
    const btn = document.getElementById("model");
    btn.textContent = modelLabel + (effort ? " · " + effort : "");
    btn.title = btn.textContent || "Switch model";
  }
  function cleanTitle(s) {
    let t = String(s || "");
    const i = t.search(/\[(repository map:|harness\])/i);
    if (i >= 0) t = t.slice(0, i);
    return t.trim();
  }
  function clipLine(s) {
    const line = cleanTitle(String(s || "").split("\n")[0]);
    return line.length > 80 ? line.slice(0, 79) + "…" : line;
  }
  function paintTitle() {
    const shown = cleanTitle(curTitle) || clipLine(lastUserText);
    document.getElementById("title").textContent = shown || "New conversation";
    document.title = (shown ? shown + " — " : "") + "janetcode";
  }
  function applyHello(h) {
    if (!h) return;
    // A different build than the page was served from means the server
    // restarted with a new binary; this JavaScript is stale no matter
    // how hard the user refreshes against the old process's memory.
    if (window.__janetUIBuild && h.version && window.__janetUIBuild !== h.version) {
      document.getElementById("reload-toast").hidden = false;
    }
    modelLabel = [h.provider, h.model].filter(Boolean).join(" · ");
    paintModel();
    setModeChip(h.mode);
    document.getElementById("cwd").textContent = h.cwd || "";
    if (h.title) curTitle = cleanTitle(h.title);
    paintTitle();
    const leaf = (h.cwd || "").replace(/\\/g, "/").split("/").filter(Boolean).pop();
    if (leaf) document.getElementById("empty-lead").textContent = "What should we change in " + leaf + "?";
    loadSessions();
  }

  const reloadBtn = document.getElementById("reload-btn");
  if (reloadBtn) reloadBtn.addEventListener("click", () => location.reload());

  function applyStatus(st) {
    if (!st) return;
    const prev = running;
    running = !!st.running;
    maybeNotify(prev, st);
    const sendBtn = document.getElementById("send");
    sendBtn.title = running ? "Queue behind the running turn" : "Send";
    // Kimi-style swap: while a turn runs the round button becomes the
    // stop button; queueing still happens with Enter.
    sendBtn.hidden = running;
    document.getElementById("stop").hidden = !running;
    const steerHint = document.getElementById("steer-hint");
    if (steerHint) steerHint.hidden = !running;
    const bits = [];
    if (st.running) bits.push((st.phase || "Working") + (st.elapsed ? " · " + st.elapsed : ""));
    else if (st.status) bits.push(st.status);
    if (st.queued) bits.push(st.queued + " waiting");
    statusEl.textContent = bits.join(" · ");
    statusEl.classList.toggle("busy", !!st.running);
    if (st.mode) setModeChip(st.mode);
    setEffortChip(st.effort);
    if (st.phase) lastFlavor = st.phase;
    relabelThinking();
    refreshWorkspace();
    // The trajectory view refetches when the turn's shape changes —
    // running flips, the phase moves — not on every status tick, so a
    // long thinking pause stops no longer re-renders the panel twice a
    // second. The view also refetches when it is opened.
    if (currentView === "trajectory" && (st.running !== traj.lastRunning || st.phase !== traj.lastPhase)) {
      refreshTrajectory();
    }
    traj.lastRunning = !!st.running;
    traj.lastPhase = st.phase || "";
  }

  function relabelThinking() {
    if (!lastFlavor) return;
    latest.forEach((e, id) => {
      if (e.kind !== "thinking" || e.done || (e.text || "").trim()) return;
      const n = nodes.get(id);
      if (n) n.innerHTML = `<span class="lbl">${esc(lastFlavor)}</span>`;
    });
  }

  function setModeChip(mode) {
    const chip = document.getElementById("mode-chip");
    if (!chip) return;
    const m = mode || "ask";
    chip.textContent = m;
    chip.dataset.mode = m;
  }

  // The reasoning tier chip is always up: it is the one control a user
  // goes looking for when the model thinks too shallow or too long, and
  // a hidden control might as well not exist.
  function setEffortChip(effort) {
    const chip = document.getElementById("effort-chip");
    if (!chip) return;
    chip.hidden = false;
    chip.dataset.effort = effort || "";
    chip.textContent = effort ? "effort " + effort : "effort";
    paintModel();
  }

  function showAsk(p) {
    pending = p && p.name ? p : null;
    ask.hidden = !pending;
    if (!pending) return;
    askLine.textContent = "Allow " + p.name + (p.summary ? " — " + p.summary : "") + (p.warning ? "  Warning: " + p.warning : "") + "?";
    askRule.hidden = !p.rule;
    if (p.rule) askRule.textContent = "rule: " + p.rule + (p.learned ? "  ·  " + p.learned : "");
    showRungs(p);
    askImpact.hidden = !p.impact;
    if (p.impact) askImpact.textContent = p.impact;
    askPreview.hidden = !p.preview;
    if (p.preview) askPreview.innerHTML = diffHTML(p.preview);
  }

  // The ladder of rules "Remember this rule" may save, narrowest first
  // (see perm.SuggestLadder). One rung is no choice at all, so the card
  // stays as it was and the line above already names the rule.
  function showRungs(p) {
    const rungs = Array.isArray(p.rungs) ? p.rungs : [];
    askRungs.hidden = rungs.length < 2;
    askRung.innerHTML = "";
    if (askRungs.hidden) return;
    rungs.forEach((r, i) => {
      const o = document.createElement("option");
      o.value = r.rule;
      o.textContent = r.rule + (r.learned ? "  ·  " + r.learned : "");
      o.selected = i === (p.ruleIndex || 0);
      askRung.appendChild(o);
    });
  }

  // chosenRule is the rung the card has selected, "" when it offers no
  // choice — the server then saves the one it proposed.
  function chosenRule() {
    return askRungs.hidden ? "" : askRung.value;
  }

  function showQuestions(qs) {
    pendingQs = Array.isArray(qs) && qs.length ? qs : [];
    qIdx = 0;
    qAnswers = [];
    questionsEl.hidden = pendingQs.length === 0;
    if (!pendingQs.length) {
      qOpts.innerHTML = "";
      qOther.hidden = true;
      return;
    }
    renderQuestion();
  }

  function renderQuestion() {
    const q = pendingQs[qIdx];
    if (!q) return;
    qPrompt.textContent = q.prompt || "Choose";
    qDetail.hidden = !q.detail;
    qDetail.textContent = q.detail || "";
    const opts = (q.options || []).concat([{ label: "Other", description: "type your own answer" }]);
    qOpts.innerHTML = opts.map((o) =>
      `<button type="button" data-label="${esc(o.label)}">${esc(o.label)}</button>`
    ).join("");
    qOther.hidden = true;
    qOtherText.value = "";
  }

  qOpts.addEventListener("click", async (ev) => {
    const b = ev.target.closest("button[data-label]");
    if (!b) return;
    if (b.dataset.label === "Other") {
      qOther.hidden = false;
      qOtherText.focus();
      return;
    }
    await pickAnswer(b.dataset.label, "");
  });
  qOther.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    await pickAnswer("Other", qOtherText.value.trim());
  });

  async function pickAnswer(label, reason) {
    qAnswers.push({ label, reason });
    qIdx++;
    if (qIdx < pendingQs.length) {
      renderQuestion();
      return;
    }
    await post("/api/question", { answers: qAnswers });
    showQuestions([]);
  }

  function handle(msg) {
    switch (msg.type) {
      case "hello": applyHello(msg.hello); break;
      case "entry": if (msg.entry) render(msg.entry); break;
      case "status": applyStatus(msg.status); break;
      case "permission": showAsk(msg.permission); break;
      case "question": showQuestions(msg.questions); break;
      case "reset":
        log.innerHTML = "";
        nodes.clear();
        latest.clear();
        openTools.clear();
        openThoughts.clear();
        openAnswers.clear();
        openRuns.clear();
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
    const mentions = chips.map((p) => "@" + p).join(" ");
    return [mentions, typed].filter(Boolean).join("\n");
  }
  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    if (!slashEl.hidden) { acceptSlash(); return; }
    const text = composedText();
    if (!text) return;
    if (!text.startsWith("/")) lastPrompt = text;
    histPush(text);
    input.value = "";
    chips.length = 0;
    paintChips();
    resize();
    acHide();
    hideSlash();
    await post("/api/prompt", { text });
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
  function acWord() {
    const pos = input.selectionStart;
    const before = input.value.slice(0, pos);
    const m = before.match(/(^|\s)@([^\s@]*)$/);
    if (!m) return null;
    return { start: pos - m[2].length, partial: m[2] };
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
    const sep = p.endsWith("/") ? "" : " ";
    input.value = input.value.slice(0, acStart) + p + sep + tail;
    input.selectionStart = input.selectionEnd = acStart + p.length + sep.length;
    acHide();
    resize();
    input.focus();
  }
  input.addEventListener("input", () => {
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
    slashItems = commands.filter((c) => {
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
    if (ev.key === "Enter" && !ev.shiftKey && (ev.ctrlKey || ev.metaKey) && running) {
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
    if (pending && input.value === "" && !ev.metaKey && !ev.ctrlKey) {
      const map = { y: "allow", a: "session", r: "always", n: "deny", c: "cancel" };
      if (map[ev.key.toLowerCase()]) {
        ev.preventDefault();
        await post("/api/permission", { decision: map[ev.key.toLowerCase()], rule: chosenRule() });
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
    await post("/api/permission", { decision: b.dataset.d, rule: chosenRule() });
  });
  document.querySelector(".switches").addEventListener("click", async (ev) => {
    const b = ev.target.closest("button[data-cmd]");
    if (!b) return;
    await post("/api/command", { text: b.dataset.cmd });
  });

  // The help sheet: what the keys do and what the header controls mean.
  // "?" opens it from anywhere; Esc closes it and, because this listener
  // is registered first, nothing else answers that Esc.
  const helpEl = document.getElementById("help");
  function openHelp() {
    helpEl.hidden = false;
  }
  function closeHelp() {
    helpEl.hidden = true;
  }
  document.getElementById("help-btn").addEventListener("click", openHelp);
  document.getElementById("help-close").addEventListener("click", closeHelp);
  document.addEventListener("keydown", (ev) => {
    if (settingsEl && !settingsEl.hidden) {
      if (ev.key === "Escape") {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        settingsEl.hidden = true;
      }
      return;
    }
    if (!helpEl.hidden) {
      if (ev.key === "Escape" || (ev.shiftKey && ev.key === "?")) {
        ev.preventDefault();
        ev.stopImmediatePropagation();
        closeHelp();
      }
      return;
    }
    if (ev.shiftKey && ev.key === "?") {
      const el = document.activeElement;
      if (el && (el.tagName === "TEXTAREA" || el.tagName === "INPUT" || el.isContentEditable)) return;
      if (!palette.hidden) return;
      ev.preventDefault();
      openHelp();
    }
  });

  // The find bar: Ctrl+F searches the conversation itself — every text
  // node in the thread, highlighted, with next/prev like the browser's
  // own find but able to see inside rendered markdown.
  const findbar = document.getElementById("findbar");
  const findQ = document.getElementById("find-q");
  const findCount = document.getElementById("find-count");
  let findHits = [];
  let findIdx = -1;

  function openFind() {
    findbar.hidden = false;
    findQ.focus();
    findQ.select();
    if (findQ.value) findRender(findQ.value);
  }
  function closeFind() {
    findbar.hidden = true;
    findClear();
  }
  function findClear() {
    for (const h of findHits) h.replaceWith(document.createTextNode(h.textContent));
    findHits = [];
    findIdx = -1;
    findCount.textContent = "";
  }
  function findRender(q) {
    findClear();
    q = q.trim();
    if (!q) return;
    const root = scroller();
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT, {
      acceptNode(n) {
        if (!n.nodeValue.trim()) return NodeFilter.FILTER_REJECT;
        const p = n.parentElement;
        if (!p || p.closest("textarea, style, script")) return NodeFilter.FILTER_REJECT;
        return NodeFilter.FILTER_ACCEPT;
      },
    });
    const nodes = [];
    while (walker.nextNode()) nodes.push(walker.currentNode);
    const lower = q.toLowerCase();
    for (const node of nodes) {
      const text = node.nodeValue;
      const tl = text.toLowerCase();
      const frag = document.createDocumentFragment();
      let i = 0,
        hit,
        last = 0;
      while ((hit = tl.indexOf(lower, i)) !== -1) {
        frag.appendChild(document.createTextNode(text.slice(last, hit)));
        const span = document.createElement("span");
        span.className = "find-hit";
        span.textContent = text.slice(hit, hit + q.length);
        frag.appendChild(span);
        findHits.push(span);
        last = hit + q.length;
        i = hit + q.length;
        if (findHits.length >= 500) break;
      }
      if (last) {
        frag.appendChild(document.createTextNode(text.slice(last)));
        node.parentNode.replaceChild(frag, node);
      }
      if (findHits.length >= 500) break;
    }
    if (!findHits.length) {
      findCount.textContent = "no matches";
      return;
    }
    findPaint(0);
  }
  function findPaint(i) {
    findIdx = (i + findHits.length) % findHits.length;
    findHits.forEach((h, j) => h.classList.toggle("on", j === findIdx));
    findHits[findIdx].scrollIntoView({ block: "center" });
    findCount.textContent = findIdx + 1 + "/" + findHits.length;
  }
  document.addEventListener("keydown", (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "f") {
      ev.preventDefault();
      if (findbar.hidden) openFind();
      else closeFind();
      return;
    }
    if (!findbar.hidden && ev.key === "Escape") {
      ev.preventDefault();
      ev.stopImmediatePropagation();
      closeFind();
      input.focus();
    }
  });
  findQ.addEventListener("input", () => findRender(findQ.value));
  findQ.addEventListener("keydown", (ev) => {
    ev.stopPropagation(); // the composer must not read these as its own
    if (ev.key === "Enter") {
      ev.preventDefault();
      findPaint(findIdx + (ev.shiftKey ? -1 : 1));
    }
    if (ev.key === "ArrowUp") { ev.preventDefault(); findPaint(findIdx - 1); }
    if (ev.key === "ArrowDown") { ev.preventDefault(); findPaint(findIdx + 1); }
  });
  document.getElementById("find-prev").addEventListener("click", () => findPaint(findIdx - 1));
  document.getElementById("find-next").addEventListener("click", () => findPaint(findIdx + 1));
  document.getElementById("find-close").addEventListener("click", closeFind);

  // The jump-to-latest pill: floated when the reader is away from the
  // tail, hidden once they are back on it.
  const toLatest = document.getElementById("to-latest");
  function paintToLatest() {
    const el = scroller();
    toLatest.hidden = el.scrollHeight - el.scrollTop - el.clientHeight <= 120;
  }
  scroller().addEventListener("scroll", paintToLatest, { passive: true });
  window.addEventListener("resize", paintToLatest);
  toLatest.addEventListener("click", () => {
    const el = scroller();
    el.scrollTop = el.scrollHeight;
    toLatest.hidden = true;
    input.focus();
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
  const attachInput = document.getElementById("attach-file");
  async function uploadFiles(list) {
    if (!list || !list.length) return;
    const fd = new FormData();
    for (const f of list) fd.append("files", f);
    const r = await fetch("/api/upload", { method: "POST", body: fd });
    if (!r.ok) { statusEl.textContent = (await r.text()) || r.statusText; return; }
    const data = await r.json();
    for (const p of data.paths || []) chips.push(p);
    paintChips();
  }
  document.getElementById("attach-btn").addEventListener("click", () => attachInput.click());
  attachInput.addEventListener("change", () => { uploadFiles(attachInput.files); attachInput.value = ""; });
  form.addEventListener("dragover", (ev) => ev.preventDefault());
  form.addEventListener("drop", (ev) => {
    ev.preventDefault();
    if (ev.dataTransfer && ev.dataTransfer.files && ev.dataTransfer.files.length) uploadFiles(ev.dataTransfer.files);
  });
  document.addEventListener("click", (ev) => {
    if (!slashEl.hidden && !ev.target.closest("#composer")) hideSlash();
  });
  document.addEventListener("keydown", (ev) => {
    if ((ev.metaKey || ev.ctrlKey) && ev.key.toLowerCase() === "k") {
      ev.preventDefault();
      if (palette.hidden) openPalette(); else closePalette();
    }
  });
  // Vi-style scrolling when the keyboard is not in a field: j/k step,
  // g/G jump. The trajectory view answers with its own ledger when that
  // is what overflows. Every key still belongs to whatever field is
  // focused — this only answers for the bare keyboard.
  document.addEventListener("keydown", (ev) => {
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    const el = document.activeElement;
    if (el && (el.tagName === "TEXTAREA" || el.tagName === "INPUT" || el.isContentEditable)) return;
    if (!palette.hidden) return;
    const thread = currentView === "trajectory" && trajLedger.scrollHeight > trajLedger.clientHeight ? trajLedger : scroller();
    if (ev.key === "j") thread.scrollBy({ top: 72 });
    else if (ev.key === "k") thread.scrollBy({ top: -72 });
    else if (ev.key === "g") thread.scrollTop = 0;
    else if (ev.key === "G") thread.scrollTop = thread.scrollHeight;
    else return;
    ev.preventDefault();
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

  function closeSwitchMenu() {
    switchMenu.hidden = true;
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
    switchQ.placeholder = "Switch model…";
    switchList.innerHTML = `<div class="menu-empty">Loading…</div>`;
    placeSwitchMenu(modelBtn);
    switchMenu.hidden = false;
    switchQ.focus();
    try {
      modelGroups = (await (await fetch("/api/models")).json()).groups || [];
    } catch (_) {
      modelGroups = [];
    }
    if (switchMenu.hidden || switchMode !== "model") return;
    renderSwitchMenu(switchQ.value);
  }

  function openEffortMenu() {
    switchMode = "effort";
    switchQ.value = "";
    switchQ.placeholder = "Set effort…";
    renderSwitchMenu("");
    placeSwitchMenu(effortChip);
    switchMenu.hidden = false;
    switchQ.focus();
  }

  function rowHTML(i, main, sub) {
    return `<div class="menu-row" data-i="${i}"><span>${esc(main)}</span>` + (sub ? `<span class="menu-sub">${esc(sub)}</span>` : "") + `</div>`;
  }

  function paintSwitchRows() {
    switchList.querySelectorAll(".menu-row").forEach((el) => el.classList.toggle("on", Number(el.dataset.i) === switchIdx));
  }

  function renderSwitchMenu(q) {
    q = q.trim().toLowerCase();
    switchRows = [];
    let html = "";
    if (switchMode === "model") {
      for (const g of modelGroups) {
        const rows = g.models.filter((m) => !q || m.model.toLowerCase().includes(q));
        if (!rows.length) continue;
        html += `<div class="menu-group">${esc(g.provider)} · ${rows.length}</div>`;
        for (const m of rows) {
          switchRows.push({ cmd: "/model " + m.model });
          const sub = [m.context ? Math.round(m.context / 1000) + "k ctx" : "", m.live ? "live" : ""].filter(Boolean).join(" · ");
          html += rowHTML(switchRows.length - 1, m.model + (m.current ? " ✓" : ""), sub);
        }
      }
      // A name nobody listed is still switchable: /model routes it by
      // family or prefix, and remembers it.
      if (q && !modelGroups.some((g) => g.models.some((m) => m.model.toLowerCase() === q))) {
        switchRows.unshift({ cmd: "/model " + q });
        html = rowHTML(0, `switch to “${q}”`, "routes by name or prefix") + html;
      }
    } else {
      const cur = (effortChip.textContent || "").replace(/^effort /, "");
      for (const t of EFFORT_TIERS) {
        if (q && !t.includes(q)) continue;
        switchRows.push({ cmd: "/effort " + t });
        html += rowHTML(switchRows.length - 1, t + (cur === t ? " ✓" : ""), "");
      }
    }
    switchList.innerHTML = html || `<div class="menu-empty">Nothing matches</div>`;
    switchIdx = 0;
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
    if (ev.key === "ArrowDown") { ev.preventDefault(); switchIdx = Math.min(switchIdx + 1, switchRows.length - 1); paintSwitchRows(); return; }
    if (ev.key === "ArrowUp") { ev.preventDefault(); switchIdx = Math.max(switchIdx - 1, 0); paintSwitchRows(); return; }
    if (ev.key === "Enter") { ev.preventDefault(); chooseSwitch(switchIdx); }
  });
  switchList.addEventListener("click", (ev) => {
    const row = ev.target.closest("[data-i]");
    if (row) chooseSwitch(Number(row.dataset.i));
  });
  document.addEventListener("click", (ev) => {
    if (switchMenu.hidden) return;
    if (!ev.target.closest("#switch-menu") && !ev.target.closest("#model") && !ev.target.closest("#effort-chip")) closeSwitchMenu();
  });
  stopBtn.addEventListener("click", async () => {
    await post("/api/cancel", {});
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
      const jobsEl = document.getElementById("jobs");
      if (ws.jobs && ws.jobs.length) {
        jobsEl.innerHTML = ws.jobs.map((j) => `<li data-job="${j.id}" title="Open log">${esc(j.summary || j.command)}</li>`).join("");
      } else jobsEl.innerHTML = `<li class="quiet">No jobs running</li>`;
      const agentsEl = document.getElementById("agents");
      if (ws.agents && ws.agents.length) {
        agentsEl.innerHTML = ws.agents.map((a) =>
          `<li class="agent ${a.done ? "done" : "running"}${a.id === selectedAgent ? " on" : ""}" data-agent="${esc(a.id)}" title="Open transcript"><span class="a-id">${esc(a.id)}</span> ${esc(a.desc || "")}` +
          (!a.done && a.last ? `<span class="a-last">${esc(a.last)}</span>` : "") + `</li>`
        ).join("");
      } else agentsEl.innerHTML = `<li class="quiet">No subagents yet</li>`;
      const ptcEl = document.getElementById("ptc");
      if (ptcEl) {
        if (ws.ptc && ws.ptc.length) {
          ptcEl.innerHTML = ws.ptc.map((p) =>
            `<li data-ptc="${p.n}" title="Open run">${esc(p.tool)} ${p.n} · ${esc(p.summary || "")}</li>`
          ).join("");
        } else ptcEl.innerHTML = `<li class="quiet">No scripts this session</li>`;
      }
      document.getElementById("ctx-line").textContent = ws.ctx_label ? "Context " + ws.ctx_label + " (" + ws.ctx_pct + "%)" : "Context —";
      const chip = document.getElementById("ctx-chip");
      if (ws.ctx_pct) {
        chip.hidden = false;
        chip.textContent = ws.ctx_pct + "% ctx";
        chip.classList.toggle("hot", ws.ctx_pct >= 80);
      } else {
        chip.hidden = true;
      }
      document.getElementById("cost-line").textContent = ws.cost ? "Spend " + ws.cost : "";
      document.getElementById("mode-yolo").classList.toggle("on", !!ws.yolo);
      document.getElementById("mode-auto").classList.toggle("on", !!ws.auto);
      document.getElementById("mode-edits").classList.toggle("on", !!ws.edits);
      document.getElementById("mode-plan").classList.toggle("on", !!ws.plan);
      document.getElementById("mode-ro").classList.toggle("on", !!ws.readonly);
      setModeChip(ws.mode);
      lastWorkspace = ws;
      paintGit(ws);
      paintTurnFiles(ws.files);
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

  function dayBucket(iso) {
    const t = Date.parse((iso || "").replace(" ", "T"));
    if (!t) return "Older";
    const day = (d) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    const diff = Math.round((day(new Date()) - day(new Date(t))) / 86400000);
    if (diff <= 0) return "Today";
    if (diff === 1) return "Yesterday";
    if (diff < 7) return diff + " days ago";
    return "Older";
  }

  // renderSessions paints the rail: the live conversation up top, then
  // the directory's past conversations under History, grouped by day,
  // filtered by the rail's search field.
  function renderSessions(rows, filter) {
    const live = rows.find((r) => r.current);
    const curEl = document.getElementById("current-session");
    if (live) {
      let label = cleanTitle(live.label || "");
      if (!label || label.startsWith("[") || label.startsWith("<")) label = clipLine(lastUserText) || "New conversation";
      curEl.innerHTML =
        `<div class="cur-row" title="The conversation open now">` +
        `<span class="cur-dot"></span><span class="cur-label">${esc(live.prefix || "")}${esc(label)}</span>` +
        `<span class="cur-live">live</span></div>`;
      curEl.hidden = false;
    } else {
      curEl.hidden = true;
    }
    const ul = document.getElementById("sessions");
    const q = (filter || "").trim().toLowerCase();
    const match = (r) =>
      !q ||
      (r.label || "").toLowerCase().includes(q) ||
      (r.id || "").toLowerCase().includes(q) ||
      (r.note || "").toLowerCase().includes(q);
    const kept = rows.filter((r) => !r.current).filter(match).slice(0, 40);
    if (!kept.length) {
      ul.innerHTML = `<li class="quiet">${q ? "Nothing matches" : "No past conversations"}</li>`;
      return;
    }
    const order = ["Today", "Yesterday", "2 days ago", "3 days ago", "4 days ago", "5 days ago", "6 days ago", "Older"];
    const seen = new Set();
    const parts = [];
    for (const r of kept) {
      const b = dayBucket(r.updated);
      if (!seen.has(b)) {
        seen.add(b);
        parts.push(`<li class="sess-group">${esc(b)}</li>`);
      }
      parts.push(
        `<li class="${r.current ? "on" : ""}" data-id="${esc(r.id)}" title="${esc(r.note || "Resume this conversation")}" style="padding-left:${8 + (r.depth || 0) * 10}px"><span>${esc(r.prefix || "")}${esc(r.label)}</span><span class="when">${esc(relTime(r.updated))}${r.note ? " · parent elsewhere" : ""}</span></li>`
      );
    }
    ul.innerHTML = parts.join("");
  }

  async function loadSessions() {
    try {
      const rows = await (await fetch("/api/sessions")).json();
      renderSessions(rows, sessionFilter.value);
    } catch (_) {}
  }

  const sessionFilter = document.getElementById("session-filter");
  sessionFilter.addEventListener("input", loadSessions);

  document.getElementById("sessions").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-id]");
    if (!li || li.classList.contains("on")) return;
    if (await post("/api/load", { id: li.dataset.id })) {
      log.innerHTML = "";
      nodes.clear();
      latest.clear();
      openTools.clear();
      openThoughts.clear();
      openAnswers.clear();
      openRuns.clear();
      if (window.matchMedia("(max-width: 960px)").matches && !shell.classList.contains("nav-collapsed")) {
        setCollapsed("nav-collapsed", true);
      }
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
    showDetail(data.path || "", data.diff || "", true);
  });
  document.getElementById("jobs").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-job]");
    if (!li) return;
    const data = await (await fetch("/api/job?id=" + li.dataset.job)).json();
    showDetail("job " + data.id + " · " + (data.command || ""), data.output || "(no output)", false);
  });
  document.getElementById("agents").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-agent]");
    if (!li) return;
    selectedAgent = li.dataset.agent;
    document.querySelectorAll("#agents li[data-agent]").forEach((row) => row.classList.toggle("on", row.dataset.agent === selectedAgent));
    const data = await (await fetch("/api/agent?id=" + encodeURIComponent(li.dataset.agent))).json();
    showDetail(data.id + " · " + (data.desc || ""), data.text || "", false);
  });
  document.getElementById("ptc").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-ptc]");
    if (!li) return;
    const data = await (await fetch("/api/ptc?n=" + li.dataset.ptc)).json();
    showDetail((data.tool || "ptc") + " " + data.n, data.text || "", false);
  });
  function showDetail(title, text, asDiff) {
    document.getElementById("viewer-path").textContent = title;
    const el = document.getElementById("viewer-diff");
    el.innerHTML = asDiff ? diffHTML(text) : esc(text);
    document.getElementById("viewer").hidden = false;
  }
  document.getElementById("viewer-close").addEventListener("click", () => {
    document.getElementById("viewer").hidden = true;
  });
  document.getElementById("undo-btn").addEventListener("click", () => post("/api/command", { text: "/undo" }).then(refreshWorkspace));
  document.getElementById("new-chat").addEventListener("click", async () => {
    if (await post("/api/new", {})) {
      log.innerHTML = "";
      nodes.clear();
      latest.clear();
      openTools.clear();
      openThoughts.clear();
      openAnswers.clear();
      openRuns.clear();
      empty.hidden = false;
      refreshWorkspace();
      loadSessions();
    }
  });

  fetch("/api/commands").then((r) => r.json()).then((c) => { commands = c || []; }).catch(() => {});
  // Save the conversation as markdown, built client-side from the
  // entries on screen — no server round trip, and it saves what you see.
  document.getElementById("export-btn").addEventListener("click", () => {
    const parts = [];
    for (const art of log.querySelectorAll("article.turn")) {
      const e = latest.get(art.dataset.id);
      if (!e) continue;
      if (e.kind === "user") parts.push("## you\n\n" + (e.text || ""));
      else if (e.kind === "assistant") parts.push("## janetcode\n\n" + (e.text || ""));
      else if (e.kind === "tool" && e.output) {
        parts.push("### " + (e.name || "tool") + " " + (e.summary || "") + "\n\n```\n" + e.output + "\n```");
      }
    }
    if (!parts.length) return;
    const blob = new Blob([parts.join("\n\n")], { type: "text/markdown" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = (curTitle || "janetcode") + ".md";
    a.click();
    URL.revokeObjectURL(a.href);
  });

  // The hamburger in the header opens the rail as a drawer on narrow
  // screens, where the rail's own toggle is hidden inside the drawer.
  document.getElementById("nav-open").addEventListener("click", () => {
    setCollapsed("nav-collapsed", !shell.classList.contains("nav-collapsed"));
  });

  // A skeleton stands in while the history loads, so resume does not
  // flash an empty page.
  log.innerHTML = `<div class="skel"></div><div class="skel skel-short"></div><div class="skel"></div>`;
  fetch("/api/state").then((r) => r.json()).then((st) => {
    log.querySelectorAll(".skel").forEach((n) => n.remove());
    applyHello(st.hello);
    applyStatus(st.status);
    (st.entries || []).forEach(render);
    showAsk(st.permission);
    refreshWorkspace();
  }).catch((err) => { console.error("BOOT FAILED", err); });
  const shell = document.querySelector(".shell");
  const themeBtn = document.getElementById("theme-toggle");
  function drawTheme() { themeBtn.innerHTML = icon(document.documentElement.dataset.theme === "dark" ? "sun" : "moon"); }
  function applyThemeChoice(choice) {
    localStorage.setItem("janetcode-theme", choice);
    document.documentElement.dataset.themeChoice = choice;
    const dark = choice === "dark" || (choice === "system" && matchMedia("(prefers-color-scheme: dark)").matches);
    document.documentElement.dataset.theme = dark ? "dark" : "light";
    if (term) term.options.theme = termTheme();
    drawTheme();
    document.querySelectorAll("#theme-seg [data-theme]").forEach((b) => b.classList.toggle("on", b.dataset.theme === choice));
  }
  themeBtn.addEventListener("click", () => {
    applyThemeChoice(document.documentElement.dataset.theme === "dark" ? "light" : "dark");
  });
  drawTheme();
  matchMedia("(prefers-color-scheme: dark)").addEventListener("change", () => {
    if (document.documentElement.dataset.themeChoice === "system") applyThemeChoice("system");
  });
  const settingsEl = document.getElementById("settings");
  let soundOn = localStorage.getItem("janetcode-sound") === "1";
  function paintSound() {
    const b = document.getElementById("sound-btn");
    if (b) b.textContent = soundOn ? "Sound on" : "Sound off";
  }
  function ding() {
    if (!soundOn) return;
    try {
      const ctx = new AudioContext();
      const o = ctx.createOscillator();
      const g = ctx.createGain();
      o.frequency.value = 880;
      o.connect(g);
      g.connect(ctx.destination);
      g.gain.setValueAtTime(0.04, ctx.currentTime);
      g.gain.exponentialRampToValueAtTime(0.001, ctx.currentTime + 0.12);
      o.start();
      o.stop(ctx.currentTime + 0.13);
    } catch (_) {}
  }
  function openSettings() {
    settingsEl.hidden = false;
    applyThemeChoice(document.documentElement.dataset.themeChoice || "dark");
    const fs = document.documentElement.dataset.fontScale || "medium";
    document.querySelectorAll("#font-seg [data-font]").forEach((b) => b.classList.toggle("on", b.dataset.font === fs));
    paintSound();
  }
  document.getElementById("settings-btn").addEventListener("click", openSettings);
  document.getElementById("settings-close").addEventListener("click", () => { settingsEl.hidden = true; });
  document.getElementById("theme-seg").addEventListener("click", (ev) => {
    const b = ev.target.closest("[data-theme]");
    if (b) applyThemeChoice(b.dataset.theme);
  });
  document.getElementById("font-seg").addEventListener("click", (ev) => {
    const b = ev.target.closest("[data-font]");
    if (!b) return;
    document.documentElement.dataset.fontScale = b.dataset.font;
    localStorage.setItem("janetcode-font-scale", b.dataset.font);
    document.querySelectorAll("#font-seg [data-font]").forEach((x) => x.classList.toggle("on", x === b));
  });
  document.getElementById("sound-btn").addEventListener("click", () => {
    soundOn = !soundOn;
    localStorage.setItem("janetcode-sound", soundOn ? "1" : "");
    paintSound();
    if (soundOn) ding();
  });
  paintSound();
  document.getElementById("mode-chip").addEventListener("click", () => {
    setCollapsed("work-collapsed", false);
    document.querySelector('.work-tabs [data-pane="session"]')?.click();
  });
  document.addEventListener("keydown", (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && ev.shiftKey && ev.key.toLowerCase() === "o") {
      ev.preventDefault();
      document.getElementById("new-chat").click();
    }
  });

  document.getElementById("empty").addEventListener("click", (ev) => {
    const chip = ev.target.closest(".chip");
    if (!chip) return;
    input.value = chip.dataset.q || "";
    resize();
    input.focus();
  });

  let currentView = "chat";
  const chatView = document.getElementById("chat-view");
  const trajView = document.getElementById("trajectory");
  const trajLedger = document.getElementById("traj-ledger");
  const trajChart = document.getElementById("traj-chart");
  const trajLanes = document.getElementById("traj-lanes");
  const trajTrack = document.getElementById("traj-track");
  const trajSel = document.getElementById("traj-sel");
  const trajSearch = document.getElementById("traj-search");
  const trajMeta = document.getElementById("traj-meta");
  const trajInspect = document.getElementById("traj-inspect");

  document.getElementById("view-ring").addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-view]");
    if (!btn) return;
    currentView = btn.dataset.view;
    document.querySelectorAll("#view-ring [data-view]").forEach((b) => b.classList.toggle("on", b === btn));
    chatView.hidden = currentView !== "chat";
    trajView.hidden = currentView !== "trajectory";
    document.getElementById("thread").hidden = currentView === "terminal";
    document.querySelector(".col").classList.toggle("show-terminal", currentView === "terminal");
    if (currentView === "trajectory") refreshTrajectory();
    if (currentView === "terminal") {
      termView.hidden = false;
      ensureTerminal();
      requestAnimationFrame(() => fitTerm());
    } else {
      termView.hidden = true;
    }
  });

  // The terminal view: xterm.js over a websocket bridged to a pty in the
  // session's directory. Built once, kept across view switches; the shell
  // dies with the socket, so leaving the view and coming back starts
  // clean only if the socket actually dropped.
  const termView = document.getElementById("terminal");
  let term = null;
  let termFit = null;
  let termWS = null;
  function termTheme() {
    const dark = document.documentElement.dataset.theme === "dark";
    return dark ? {
      background: "#101014",
      foreground: "rgba(238,238,245,0.9)",
      cursor: "#4563ee",
      selectionBackground: "#2a2a35",
    } : {
      background: "#ffffff",
      foreground: "#1f2328",
      cursor: "#3b5bdb",
      selectionBackground: "#d6defc",
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
    };
  }

  // The trajectory panel: the folded ledger (a call and its result are
  // one record) with a span chart over it. The chart is plain DOM — one
  // absolutely positioned span per record inside a container whose
  // left/width implement zoom, so zooming never re-lays out the spans.
  // Dragging brushes a range that dims rather than hides, like the
  // DeepSeek harness this view is modeled on.
  const traj = {
    mode: "sequence",
    records: [],
    turns: [],
    usage: null,
    tl: null, // {spans, turns, start, end} for the current mode
    selected: 0,
    range: null, // committed brush {start, end} in domain units
    draft: null, // in-progress drag {start, end}
    view: null, // zoom window {start, end}; null is the whole domain
    lastRunning: false,
    lastPhase: "",
    spanEls: new Map(), // record index -> span element, for hit tests
  };

  function trajViewWindow() {
    if (!traj.tl) return null;
    return traj.view || { start: traj.tl.start, end: traj.tl.end };
  }

  async function refreshTrajectory() {
    try {
      const [led, tl] = await Promise.all([
        fetch("/api/trajectory").then((r) => r.json()),
        fetch("/api/trajectory/timeline?mode=" + traj.mode).then((r) => (r.ok ? r.json() : null)),
      ]);
      traj.records = led.records || [];
      traj.turns = led.turns || [];
      traj.usage = led.usage || null;
      traj.tl = tl && tl.spans ? tl : null;
      renderTrajectory();
    } catch (_) {}
  }

  function fmtTok(n) {
    if (!n) return "";
    return n >= 1000 ? (n / 1000).toFixed(1).replace(/\.0$/, "") + "k" : String(n);
  }

  function fmtDur(ms) {
    if (!ms || ms <= 0) return "";
    if (ms < 1000) return Math.round(ms) + "ms";
    const s = ms / 1000;
    if (s < 60) return (s >= 10 ? Math.round(s) : s.toFixed(1)) + "s";
    return Math.floor(s / 60) + "m" + Math.round(s % 60) + "s";
  }

  // Content is the row without the gutter: what it did, and for a tool
  // what came back — the same words the terminal ledger prints.
  function trajContent(r) {
    const parts = [];
    if (r.text) parts.push(r.text);
    if (r.preview) parts.push(r.preview);
    let body = parts.join(" · ");
    if (r.denied) body += " → refused" + (r.result ? ": " + r.result : "");
    else if (r.running) body += " → …";
    else if (r.kind === "tool" && !r.result) body += " → (no output)";
    else if (r.result) body += (r.error ? " ✗ " : " → ") + r.result;
    return body;
  }

  function trajMetrics(r) {
    const bits = [];
    if (r.usage) {
      const u = r.usage;
      if (u.in) bits.push("↑" + fmtTok(u.in));
      if (u.cache_read) bits.push("⧉" + fmtTok(u.cache_read));
      if (u.out) bits.push("↓" + fmtTok(u.out));
    }
    const d = fmtDur(r.duration_ms);
    if (d) bits.push(d);
    return bits.join(" ");
  }

  function trajMatches() {
    const q = trajSearch.value.trim().toLowerCase();
    if (!q) return null;
    const words = q.split(/\s+/);
    const set = new Set();
    for (const r of traj.records) {
      const hay = [r.text, r.preview, r.result, r.tool, r.intent, r.model, r.kind, "turn " + r.turn, "step " + r.step]
        .join("\n")
        .toLowerCase();
      if (words.every((w) => hay.includes(w))) set.add(r.index);
    }
    return set;
  }

  function trajRangeSet() {
    if (!traj.range || !traj.tl) return null;
    const set = new Set();
    for (const s of traj.tl.spans) {
      if (s.start <= traj.range.end && s.end >= traj.range.start) set.add(s.index);
    }
    return set;
  }

  function renderTrajectory() {
    if (!traj.records.length) {
      trajChart.hidden = true;
      trajMeta.textContent = "";
      trajLedger.innerHTML = `<p class="quiet">No trajectory events yet</p>`;
      trajInspect.hidden = true;
      return;
    }
    if (traj.selected && !traj.records.some((r) => r.index === traj.selected)) traj.selected = 0;
    renderTrajMeta();
    renderTrajChart();
    renderTrajLedger();
  }

  function renderTrajMeta() {
    const bits = [traj.records.length + " records", traj.turns.length + (traj.turns.length === 1 ? " turn" : " turns")];
    if (traj.usage) {
      const u = traj.usage;
      const inTok = u.in + u.cache_read + u.cache_write;
      if (inTok || u.out) bits.push("↑" + fmtTok(inTok) + " ↓" + fmtTok(u.out));
      if (u.cost_usd) bits.push("$" + u.cost_usd.toFixed(4));
    }
    const m = trajMatches();
    if (m) bits.push(m.size + " matching");
    if (traj.range) bits.push("brushed");
    trajMeta.textContent = bits.join(" · ");
  }

  // ---- the chart ----------------------------------------------------

  function renderTrajChart() {
    const tl = traj.tl;
    if (!tl || !tl.spans || !tl.spans.length || !(tl.end > tl.start)) {
      trajChart.hidden = true;
      return;
    }
    trajChart.hidden = false;
    const full = tl.end - tl.start;
    const lanes = [
      { no: 0, name: "input", cls: "lane-input" },
      { no: 1, name: "model", cls: "lane-model" },
      { no: 2, name: "tools", cls: "lane-tools" },
    ].filter((l) => tl.spans.some((s) => s.lane === l.no));
    const pct = (v) => ((v - tl.start) / full) * 100;
    const laneRows = lanes
      .map(
        (l) =>
          `<div class="traj-lane">${tl.spans
            .filter((s) => s.lane === l.no)
            .map((s) => {
              const left = pct(s.start);
              const width = Math.max(pct(s.end) - left, 0.25);
              const ttft = s.kind === "message" && s.ttft ? ` style="--ttft:${s.ttft};left:${left}%;width:${width}%"` : ` style="left:${left}%;width:${width}%"`;
              return `<span class="sp ${l.cls}${s.error ? " err" : ""}" data-index="${s.index}"${ttft} title="#${s.index} ${esc(s.label)}"></span>`;
            })
            .join("")}</div>`
      )
      .join("");
    const ticks = tl.turns
      .map((b) => {
        const p = pct(b.time);
        return `<span class="tick" style="left:${p}%"></span><span class="tlabel" style="left:${p}%">t${b.turn}</span>`;
      })
      .join("");
    const legend = lanes
      .map((l) => `<span class="lg ${l.cls}">■ ${l.name}</span>`)
      .concat([`<span class="lg lg-mode">${esc(traj.mode)}${traj.mode === "sequence" ? " · one unit per record" : " · " + esc(fmtDur(full))}</span>`])
      .join(" ");
    trajLanes.innerHTML = `<div class="traj-rulerrow">${ticks}</div>${laneRows}`;
    document.getElementById("traj-ruler").innerHTML = legend;
    traj.spanEls = new Map(
      [...trajLanes.querySelectorAll(".sp")].map((el) => [Number(el.dataset.index), el])
    );
    positionTrajView();
    paintTrajFocus();
  }

  // positionTrajView moves the one container the spans live in — the
  // CSS-zoom trick: zoom pans by stretching, never by re-laying out.
  function positionTrajView() {
    const tl = traj.tl;
    if (!tl || trajChart.hidden) return;
    const vw = trajViewWindow();
    const viewDur = vw.end - vw.start;
    const full = tl.end - tl.start;
    trajLanes.style.left = -((vw.start - tl.start) / viewDur) * 100 + "%";
    trajLanes.style.width = (full / viewDur) * 100 + "%";
    const sel = traj.draft || traj.range;
    if (!sel) {
      trajSel.hidden = true;
      return;
    }
    const left = ((sel.start - vw.start) / viewDur) * 100;
    const width = ((sel.end - sel.start) / viewDur) * 100;
    trajSel.hidden = left > 100 || left + width < 0;
    trajSel.style.left = left + "%";
    trajSel.style.width = Math.max(width, 0.4) + "%";
  }

  function paintTrajFocus() {
    const inRange = trajRangeSet();
    const matches = trajMatches();
    trajLedger.querySelectorAll(".traj-row").forEach((row) => {
      const i = Number(row.dataset.index);
      row.classList.toggle("dim", !!(inRange && !inRange.has(i)) || !!(matches && !matches.has(i)));
    });
    traj.spanEls.forEach((el, i) => {
      el.classList.toggle("dim", !!(inRange && !inRange.has(i)) || !!(matches && !matches.has(i)));
      el.classList.toggle("on", i === traj.selected);
    });
  }

  function trajPxToDomain(x) {
    const rect = trajTrack.getBoundingClientRect();
    const vw = trajViewWindow();
    return vw.start + ((x - rect.left) / rect.width) * (vw.end - vw.start);
  }

  function trajSpanAt(domainX) {
    // Topmost last wins: spans drawn later sit visually on top within a lane.
    let hit = null;
    for (const s of traj.tl.spans) {
      if (s.start <= domainX && s.end >= domainX) hit = s;
    }
    return hit;
  }

  let trajDrag = null; // {kind, button, anchorX, anchorDomain, viewStart, moved}

  trajTrack.addEventListener("contextmenu", (ev) => ev.preventDefault());
  trajTrack.addEventListener("pointerdown", (ev) => {
    if (!traj.tl || trajChart.hidden) return;
    const kind = ev.button === 2 ? "pan" : "brush";
    trajDrag = {
      kind,
      anchorX: ev.clientX,
      anchorDomain: trajPxToDomain(ev.clientX),
      viewStart: trajViewWindow().start,
      moved: false,
    };
    trajTrack.setPointerCapture(ev.pointerId);
    ev.preventDefault();
  });
  trajTrack.addEventListener("pointermove", (ev) => {
    if (!trajDrag) return;
    if (Math.abs(ev.clientX - trajDrag.anchorX) > 3) trajDrag.moved = true;
    if (trajDrag.kind === "brush") {
      const cur = trajPxToDomain(ev.clientX);
      traj.draft = { start: Math.min(trajDrag.anchorDomain, cur), end: Math.max(trajDrag.anchorDomain, cur) };
    } else {
      const rect = trajTrack.getBoundingClientRect();
      const vw = trajViewWindow();
      const dur = vw.end - vw.start;
      const tl = traj.tl;
      let start = trajDrag.viewStart - ((ev.clientX - trajDrag.anchorX) / rect.width) * dur;
      start = Math.max(tl.start, Math.min(start, tl.end - dur));
      traj.view = { start, end: start + dur };
    }
    positionTrajView();
  });
  trajTrack.addEventListener("pointerup", (ev) => {
    if (!trajDrag) return;
    const drag = trajDrag;
    trajDrag = null;
    if (drag.kind === "pan") {
      if (!drag.moved) {
        traj.range = null;
        traj.draft = null;
        positionTrajView();
        paintTrajFocus();
        renderTrajMeta();
      }
      return;
    }
    traj.draft = null;
    if (!drag.moved) {
      // A click, not a brush: on a span it selects the record and drops
      // the brush; on whitespace it snaps a one-record window around the
      // click and focuses the nearest record.
      const at = trajPxToDomain(ev.clientX);
      const hit = trajSpanAt(at);
      if (hit) {
        traj.range = null;
        positionTrajView();
        paintTrajFocus();
        selectTrajRecord(hit.index, true);
      } else {
        const tl = traj.tl;
        const minW = (tl.end - tl.start) / Math.max(tl.spans.length, 1);
        const start = Math.max(tl.start, Math.min(at - minW / 2, tl.end - minW));
        traj.range = { start, end: start + minW };
        positionTrajView();
        paintTrajFocus();
        let nearest = null;
        for (const s of tl.spans) {
          if (!nearest || Math.abs(s.start - at) < Math.abs(nearest.start - at)) nearest = s;
        }
        if (nearest) selectTrajRecord(nearest.index, true);
        renderTrajMeta();
      }
      return;
    }
    traj.range = { start: Math.min(drag.anchorDomain, trajPxToDomain(ev.clientX)), end: Math.max(drag.anchorDomain, trajPxToDomain(ev.clientX)) };
    positionTrajView();
    paintTrajFocus();
    renderTrajMeta();
  });
  trajTrack.addEventListener(
    "wheel",
    (ev) => {
      if (!traj.tl || trajChart.hidden) return;
      ev.preventDefault();
      const tl = traj.tl;
      const full = tl.end - tl.start;
      const vw = trajViewWindow();
      const dur = vw.end - vw.start;
      const minDur = Math.max((full / Math.max(tl.spans.length, 1)) * 4, traj.mode === "sequence" ? 0 : 20);
      const next = Math.min(Math.max(dur * Math.exp(ev.deltaY * 0.0015), minDur), full);
      if (next >= full * 0.999) {
        traj.view = null;
      } else {
        const anchor = trajPxToDomain(ev.clientX);
        let start = anchor - ((anchor - vw.start) / dur) * next;
        start = Math.max(tl.start, Math.min(start, tl.end - next));
        traj.view = { start, end: start + next };
      }
      positionTrajView();
    },
    { passive: false }
  );
  trajChart.addEventListener("dblclick", () => {
    traj.view = null;
    traj.range = null;
    traj.draft = null;
    positionTrajView();
    paintTrajFocus();
    renderTrajMeta();
  });

  // ---- the ledger ---------------------------------------------------

  function renderTrajLedger() {
    const headings = new Map(traj.turns.map((t) => [t.turn, t.heading || "turn " + t.turn]));
    const groups = [];
    for (const r of traj.records) {
      if (!groups.length || groups[groups.length - 1].turn !== r.turn) groups.push({ turn: r.turn, rows: [] });
      groups[groups.length - 1].rows.push(r);
    }
    trajLedger.innerHTML = groups
      .map(
        (g) =>
          `<div class="traj-group"><h3>${esc(headings.get(g.turn) || (g.turn ? "turn " + g.turn : "before the first turn"))}</h3>` +
          g.rows
            .map((r) => {
              const state = r.error ? " fail" : r.denied ? " denied" : r.running ? " running" : "";
              return `<div class="traj-row${state}" data-index="${r.index}" data-kind="${esc(r.kind)}">` +
                `<span class="seq">#${r.index}</span>` +
                `<span class="kind kind-${esc(r.kind)}">${esc(r.tag)}</span>` +
                `<span class="preview">${esc(trajContent(r))}</span>` +
                `<span class="metrics">${esc(trajMetrics(r))}</span></div>`;
            })
            .join("") +
          `</div>`
      )
      .join("");
    paintTrajFocus();
  }

  trajLedger.addEventListener("click", (ev) => {
    const row = ev.target.closest(".traj-row[data-index]");
    if (!row) return;
    selectTrajRecord(Number(row.dataset.index), false);
  });

  async function selectTrajRecord(index, scroll) {
    traj.selected = index;
    const row = trajLedger.querySelector(`.traj-row[data-index="${index}"]`);
    trajLedger.querySelectorAll(".traj-row.on").forEach((n) => n.classList.toggle("on", n === row));
    if (scroll && row) row.scrollIntoView({ block: "nearest" });
    traj.spanEls.forEach((el, i) => el.classList.toggle("on", i === index));
    try {
      const res = await fetch("/api/trajectory?n=" + index);
      if (!res.ok) {
        trajInspect.hidden = true;
        return;
      }
      const d = (await res.json()).record;
      document.getElementById("traj-inspect-title").textContent = "#" + d.index + " " + d.tag + (d.tool ? " · " + d.tool : "");
      const facts = [];
      facts.push("turn " + d.turn + (d.step ? " · step " + d.step : ""));
      if (d.denied) facts.push("refused");
      else if (d.running) facts.push("never answered");
      else if (d.error) facts.push("failed");
      if (d.model) facts.push(d.model);
      if (d.duration_ms) facts.push(fmtDur(d.duration_ms));
      if (d.ttft_ms || d.decode_ms) {
        let split = "first token " + fmtDur(d.ttft_ms) + " · decoded " + fmtDur(d.decode_ms);
        if (d.tok_per_s) split += " · " + d.tok_per_s.toFixed(1) + " tok/s";
        facts.push(split);
      }
      if (d.usage) {
        const u = d.usage;
        facts.push("↑" + fmtTok(u.in) + " (⧉" + fmtTok(u.cache_read) + ") ↓" + fmtTok(u.out) + (u.cost_usd ? " · $" + u.cost_usd.toFixed(4) : ""));
      }
      if (d.total) facts.push("session so far ↑" + fmtTok(d.total.in + d.total.cache_read + d.total.cache_write) + " ↓" + fmtTok(d.total.out));
      document.getElementById("traj-inspect-facts").innerHTML = facts.map((f) => `<span>${esc(f)}</span>`).join("");
      const body = [];
      if (d.input) body.push("input\n" + d.input);
      if (d.output) body.push((d.kind === "message" ? "answer" : "output") + "\n" + d.output);
      document.getElementById("traj-inspect-body").textContent = body.join("\n\n");
      trajInspect.hidden = false;
    } catch (_) {}
  }

  document.getElementById("traj-inspect-close").addEventListener("click", () => {
    trajInspect.hidden = true;
    trajLedger.querySelectorAll(".traj-row.on").forEach((n) => n.classList.remove("on"));
    traj.spanEls.forEach((el) => el.classList.remove("on"));
    traj.selected = 0;
  });

  trajSearch.addEventListener("input", () => {
    paintTrajFocus();
    renderTrajMeta();
  });

  document.getElementById("traj-modes").addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-mode]");
    if (!btn || btn.dataset.mode === traj.mode) return;
    traj.mode = btn.dataset.mode;
    traj.range = null;
    traj.draft = null;
    traj.view = null;
    document.querySelectorAll("#traj-modes [data-mode]").forEach((b) => b.classList.toggle("on", b === btn));
    refreshTrajectory();
  });

  document.addEventListener("keydown", (ev) => {
    if (currentView !== "trajectory") return;
    if (ev.key === "/" && document.activeElement !== trajSearch) {
      ev.preventDefault();
      trajSearch.focus();
      return;
    }
    // Escape in the search field is the field's own clear-and-blur; the
    // view's Escape is for the brush, the zoom and the inspector.
    if (ev.key !== "Escape" || document.activeElement === trajSearch) return;
    if (!trajInspect.hidden) {
      document.getElementById("traj-inspect-close").click();
    } else if (traj.range) {
      traj.range = null;
      positionTrajView();
      paintTrajFocus();
      renderTrajMeta();
    } else if (traj.view) {
      traj.view = null;
      positionTrajView();
    }
  });

  const navToggle = document.getElementById("nav-toggle");
  const workToggle = document.getElementById("work-toggle");
  const narrow = window.matchMedia("(max-width: 960px)");
  function setCollapsed(cls, on) {
    shell.classList.toggle(cls, on);
    // On a narrow screen the rail is a transient overlay; opening or
    // closing it there says nothing about the desktop layout.
    if (cls === "nav-collapsed" && narrow.matches) return;
    localStorage.setItem("janetcode-" + cls, on ? "1" : "");
  }
  shell.addEventListener("click", (ev) => {
    if (ev.target === shell && narrow.matches) setCollapsed("nav-collapsed", true);
  });
  narrow.addEventListener("change", () => {
    shell.classList.toggle("nav-collapsed", narrow.matches || localStorage.getItem("janetcode-nav-collapsed") === "1");
  });
  navToggle.addEventListener("click", () => setCollapsed("nav-collapsed", !shell.classList.contains("nav-collapsed")));
  workToggle.addEventListener("click", () => setCollapsed("work-collapsed", !shell.classList.contains("work-collapsed")));
  // The work rail starts closed. The session rail starts open, and only
  // stays shut when this browser was told to shut it.
  if (localStorage.getItem("janetcode-nav-collapsed") === "1" || narrow.matches) shell.classList.add("nav-collapsed");
  if (localStorage.getItem("janetcode-work-collapsed") !== "") shell.classList.add("work-collapsed");
  document.querySelector(".work-tabs").addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-pane]");
    if (!btn) return;
    const pane = btn.dataset.pane;
    document.querySelectorAll(".work-tabs [data-pane]").forEach((b) => b.classList.toggle("on", b === btn));
    document.querySelectorAll(".work-pane").forEach((p) => { p.hidden = p.dataset.pane !== pane; });
  });

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
    // The drawer slides on .shell.pv-open; hiding it with [hidden] would
    // kill the transition, so the class is the whole story.
    shell.classList.toggle("pv-open", open);
    pvToggle.classList.toggle("on", open);
    localStorage.setItem("janetcode-preview-open", open ? "1" : "");
    if (open && pvFrame.hidden && pvUrl.value) pvLoad(pvUrl.value);
  }
  pvToggle.addEventListener("click", () => pvOpen(!shell.classList.contains("pv-open")));
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

  // Drag-to-resize for the three panels: the nav rail widens as the
  // pointer moves right (dir 1), the right-edge drawers widen as it
  // moves left (dir -1). Widths persist per browser; double-clicking a
  // handle resets that panel to its default.
  function dragResize(handle, dir, getW, setW, min, max, storageKey) {
    if (!handle) return;
    handle.addEventListener("pointerdown", (ev) => {
      if (ev.button !== 0) return;
      ev.preventDefault();
      const startX = ev.clientX;
      const startW = getW();
      handle.setPointerCapture(ev.pointerId);
      document.body.classList.add("resizing");
      const move = (e) => {
        const w = Math.round(startW + (e.clientX - startX) * dir);
        setW(Math.max(min, Math.min(max, w)));
      };
      const up = () => {
        handle.removeEventListener("pointermove", move);
        handle.removeEventListener("pointerup", up);
        handle.removeEventListener("pointercancel", up);
        document.body.classList.remove("resizing");
        try { localStorage.setItem(storageKey, String(Math.round(getW()))); } catch (_) {}
      };
      handle.addEventListener("pointermove", move);
      handle.addEventListener("pointerup", up);
      handle.addEventListener("pointercancel", up);
    });
    handle.addEventListener("dblclick", () => {
      setW(0);
      try { localStorage.removeItem(storageKey); } catch (_) {}
    });
  }
  const navPanel = document.querySelector(".nav");
  const workPanel = document.getElementById("work");
  // The open drawer yields the thread/composer width via these vars (see
  // .shell rules in app.css); they track any drag-resize.
  const setWorkVar = (w) => {
    workPanel.style.width = w ? w + "px" : "";
    if (w) shell.style.setProperty("--work-w-actual", w + "px");
    else shell.style.removeProperty("--work-w-actual");
  };
  const setPvVar = (w) => {
    pvPane.style.width = w ? w + "px" : "";
    if (w) shell.style.setProperty("--pv-w-actual", w + "px");
    else shell.style.removeProperty("--pv-w-actual");
  };
  dragResize(document.getElementById("nav-resize"), 1,
    () => navPanel.getBoundingClientRect().width,
    (w) => document.documentElement.style.setProperty("--nav-w", (w || 268) + "px"),
    200, 440, "janetcode-nav-w");
  dragResize(document.getElementById("work-resize"), -1,
    () => workPanel.getBoundingClientRect().width,
    setWorkVar,
    260, 640, "janetcode-work-w");
  dragResize(document.getElementById("pv-resize"), -1,
    () => pvPane.getBoundingClientRect().width,
    setPvVar,
    320, 960, "janetcode-preview-w");
  try {
    const savedNav = parseInt(localStorage.getItem("janetcode-nav-w") || "", 10);
    if (savedNav >= 200 && savedNav <= 440) document.documentElement.style.setProperty("--nav-w", savedNav + "px");
    const savedWork = parseInt(localStorage.getItem("janetcode-work-w") || "", 10);
    if (savedWork >= 260 && savedWork <= 640) setWorkVar(savedWork);
    const savedPv = parseInt(localStorage.getItem("janetcode-preview-w") || "", 10);
    if (savedPv >= 320 && savedPv <= 960) setPvVar(savedPv);
  } catch (_) {}
  log.addEventListener("click", async (ev) => {
    const codecopy = ev.target.closest(".codecopy");
    if (codecopy) {
      const src = JanetMD.codeBlocks.get(codecopy.dataset.bid);
      if (src != null) {
        try {
          await navigator.clipboard.writeText(src);
          codecopy.textContent = "Copied";
          setTimeout(() => { codecopy.textContent = "Copy"; }, 1500);
        } catch (_) {}
      }
      return;
    }
    const editBtn = ev.target.closest("[data-edit]");
    if (editBtn) {
      const src = latest.get(editBtn.dataset.edit);
      if (src) {
        input.value = src.text || "";
        resize();
        input.focus();
      }
      return;
    }
    if (ev.target.closest("[data-regen]")) {
      if (lastUserText) await post("/api/prompt", { text: lastUserText });
      return;
    }
    const copy = ev.target.closest(".copy-btn");
    if (copy) {
      const src = latest.get(copy.dataset.copy);
      if (src) {
        try {
          await navigator.clipboard.writeText(src.text || "");
          copy.innerHTML = icon("check");
          copy.classList.add("done");
          setTimeout(() => { copy.innerHTML = icon("copy"); copy.classList.remove("done"); }, 1500);
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
    if (!prevRunning || st.running || !document.hidden) return;
    if (soundOn) ding();
    if (!notifyOn) return;
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
