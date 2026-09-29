(() => {
  const { icon, esc, diffHTML, perfCardHTML, usageCardHTML, linkCards } = window.JanetRender;
  // The terminal's number formats (format.js, checked against Go's).
  const fmtTok = JanetFmt.tokens;
  const fmtDur = JanetFmt.duration;
  const log = document.getElementById("log");
  const empty = document.getElementById("empty");
  let suggestedFor = null; // the cwd the empty page's chips were read for
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
  // The conversation's first prompt names it until the server titles it,
  // as the rail does; the latest prompt renamed the header every turn.
  let firstUserText = "";
  let lastFlavor = "";

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

  let lastCreated = 0;
  function render(e) {
    if (e.gone) {
      // The server dropped it from the thread (an empty thinking
      // placeholder, discarded text): drop the row too.
      const g = nodes.get(e.id);
      if (g) g.remove();
      nodes.delete(e.id);
      latest.delete(e.id);
      return;
    }
    latest.set(e.id, e);
    empty.hidden = true;
    let n = nodes.get(e.id);
    const was = stick();
    if (!n) {
      n = document.createElement("article");
      n.className = "turn";
      n.dataset.id = e.id;
      // An entry that arrives on its own rises into place. One of a burst
      // (a page load replaying the thread, a batch after a reconnect) is
      // simply there: forty rows sliding in at once is noise, not motion.
      const now = performance.now();
      if (now - lastCreated > 60 && log.childElementCount > 0) n.dataset.enter = "";
      lastCreated = now;
      log.appendChild(n);
      nodes.set(e.id, n);
    }
    if (e.at) n.title = new Date(e.at).toLocaleString();
    if (e.kind === "user") {
      n.className = "turn you";
      lastUserText = e.text || "";
      if (!firstUserText) firstUserText = lastUserText;
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
      const more = clipped ? `<button type="button" class="continue" data-more="${e.id}">Show full reply</button>` : "";
      n.innerHTML = `<div class="md${clipped ? " clip" : ""}">${JanetMD.render(e.text || "", e.done)}${e.done ? "" : '<span class="caret">▍</span>'}</div>${more}${metaRow}${e.done ? linkCards(e.text) : ""}`;
      if (e.done) {
        lastReply = (e.text || "").slice(0, 200);
        drawMermaid(n);
      }
    } else if (e.kind === "thinking") {
      n.className = "turn thought" + (e.done ? "" : " live");
      n.dataset.thought = e.id;
      const raw = (e.text || "").trim();
      if (!raw && !e.done) {
        n.innerHTML = `<span class="lbl">${esc(lastFlavor || "Thinking")}</span>`;
      } else if (e.done && !openThoughts.has(e.id)) {
        const first = raw.split("\n")[0] || "";
        // Say there is more: a first line cut mid-sentence read as the
        // whole thought.
        const line = first.length > 160 || raw.length > first.length ? first.slice(0, 160).trimEnd() + "…" : first;
        n.innerHTML = `<span class="lbl">Thought <span class="chev">${icon("chevron-right")}</span></span>${esc(line)}`;
      } else if (e.done) {
        n.innerHTML = `<span class="lbl">Thought <span class="chev">${icon("chevron-down")}</span></span><pre class="thought-body">${esc(raw)}</pre>`;
      } else {
        // The whole stream in a short window pinned to its newest line, so
        // it reads as text arriving rather than a fragment that jumps.
        n.innerHTML = `<span class="lbl">Thinking</span><div class="thought-live"><pre class="thought-body">${esc(raw)}</pre></div>`;
      }
    } else if (e.kind === "tool") {
      const folded = !!(e.run && !e.run_live && !openRuns.has(e.run));
      const kind = toolKind(e.name || "");
      n.className = "turn tool-turn" + (e.run && !e.run_head ? " run-child" : "");
      n.hidden = !!(e.run && !e.run_head && folded);
      const st = e.denied ? "Declined" : e.error ? "Failed" : e.done ? "Done" : e.running ? "Running" : "Queued";
      const stCls = "st-" + st.toLowerCase();
      // An edit shows what it changed without a click: that is the work.
      // Everything else opens on demand. openTools holds the calls whose
      // default the reader flipped.
      const openByDefault = (kind.k === "edit" || kind.k === "write") && !!e.preview && !e.denied;
      const open = openByDefault !== openTools.has(e.id);
      let bar = "";
      if (e.run_head) {
        // Only the "(n failed)" count is red: the bar is a summary, and
        // the failed call inside it carries its own red mark.
        const label = esc(e.run_label || "").replace(/\(\d+ failed\)/g, '<span class="bad">$&</span>');
        bar = `<button type="button" class="run-bar" data-run="${esc(e.run)}"><span class="chev">${icon(folded ? "chevron-right" : "chevron-down")}</span><span>${label}</span></button>`;
      }
      const bodyHidden = e.run_head && folded;
      const stat = kind.k === "edit" || kind.k === "write" ? diffStat(e.preview) : null;
      const statHTML = stat && (stat.add || stat.del)
        ? `<span class="dstat">${stat.add ? `<span class="a">+${stat.add}</span>` : ""}${stat.del ? `<span class="d">−${stat.del}</span>` : ""}</span>` : "";
      const mark = st === "Done" ? "" : `<span class="st">${st}</span>`;
      const expandable = !!(e.preview || e.output);
      let html = bar + `<div class="tool ${stCls} tk-${kind.k}${open ? " open" : ""}${expandable ? "" : " flat"}"${bodyHidden ? " hidden" : ""}>` +
        `<div class="head"><span class="ti">${icon(kind.icon)}</span><b>${esc(e.name || "")}</b><span class="sum">${esc(e.summary || "")}</span>` +
        `<span class="tail">${statHTML}${e.dur ? `<span class="dur">${esc(e.dur)}</span>` : ""}${mark}${expandable ? `<span class="chev">${icon(open ? "chevron-down" : "chevron-right")}</span>` : ""}</span></div>`;
      if (open && e.preview) html += `<pre class="diff">${diffHTML(e.preview)}</pre>`;
      if (open && e.output) {
        html += kind.k === "cmd"
          ? `<div class="term"><div class="term-cmd"><span class="prompt">$</span> ${esc(e.summary || "")}</div><pre class="out">${JanetMD.renderOutput(e.name || "", e.summary || "", e.output)}</pre></div>`
          : `<pre class="out">${JanetMD.renderOutput(e.name || "", e.summary || "", e.output)}</pre>`;
      }
      html += `</div>`;
      n.innerHTML = html;
      const head = n.querySelector(".tool .head");
      if (head && expandable) head.onclick = () => {
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
      const x = e.data && e.data.headline ? e.data : null;
      // Retry only where sending the same thing again can help: a
      // rejected key or a missing model fails the same way every time.
      const retry = (lastPrompt && (!x || x.retry) ? `<button type="button" class="retry-btn" data-retry="1">Retry</button>` : "") +
        (x && x.fix === "connect" ? `<button type="button" class="fix-btn">Fix connection</button>` : "");
      if (x) {
        const meta = [x.code, x.reason].filter(Boolean).join(" · ");
        n.innerHTML = `<div class="err-head">${esc(x.headline)}${retry}</div><div class="err-next">${esc(x.next || "")}</div>` +
          (meta ? `<div class="err-meta">${esc(meta)}</div>` : "");
      } else {
        n.innerHTML = esc(e.text || "") + (retry ? " " + retry : "");
      }
    } else if (e.kind === "verdict") {
      // The server reads the tone; the page only colours it.
      n.className = "turn verdict" + (e.tone === "ok" ? " ok" : e.tone === "bad" ? " bad" : "");
      n.textContent = e.text || "";
    } else {
      n.className = "turn note";
      n.textContent = e.text || "";
    }
    follow(was);
    paintToLatest();
  }

  // toolKind sorts a tool into what it does, for its icon and for how its
  // card opens. Names are the harness's own and MCP's (server__tool).
  function toolKind(name) {
    const n = name.toLowerCase();
    if (/^(edit_file|edit|multi_edit|apply_patch|replace)$/.test(n)) return { k: "edit", icon: "pencil" };
    if (/^(write_file|write|create_file)$/.test(n)) return { k: "write", icon: "file-plus" };
    if (/^(bash|shell|run|job|jobs)$/.test(n)) return { k: "cmd", icon: "terminal" };
    if (/^(read_file|read|read_many|list_dir|ls|glob|grep|search|find|repo_map|expand_output|symbols|references|definition)$/.test(n)) return { k: "read", icon: /grep|search|find|glob|references|definition|symbols/.test(n) ? "search" : "file" };
    if (/^(fetch_url|web_fetch|web_search|browse)$/.test(n)) return { k: "web", icon: "globe" };
    if (/^(task|agent|subagent)$/.test(n)) return { k: "task", icon: "bot" };
    return { k: "tool", icon: "wrench" };
  }

  // diffStat counts the lines a preview adds and removes.
  function diffStat(src) {
    let add = 0, del = 0;
    for (const l of String(src || "").split("\n")) {
      if (l.startsWith("+") && !l.startsWith("+++")) add++;
      else if (l.startsWith("-") && !l.startsWith("---")) del++;
    }
    return { add, del };
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
  // connected is false on a first run with no key; the header says so and
  // a prompt opens the connect form instead of going nowhere.
  let connected = true;
  let modelProvider = "";
  function paintModel() {
    const effort = document.getElementById("effort-chip")?.dataset.effort || "";
    const btn = document.getElementById("model");
    // The green dot says the page reaches janetcode; with no model behind
    // it, green next to "Not connected" read as the opposite.
    document.getElementById("conn")?.classList.toggle("idle", !connected);
    if (!connected) {
      btn.textContent = "Not connected";
      btn.title = "Connect a provider";
      btn.classList.add("unconnected");
      return;
    }
    btn.classList.remove("unconnected");
    const model = modelLabel.slice(modelProvider ? modelProvider.length + 3 : 0);
    // The provider is the part a narrow header can drop.
    // A phone shows the model's own name ("deepseek-v4-flash"): the
    // vendor prefix is what got the pill cut to "deepseek/d…".
    const slash = model.lastIndexOf("/");
    const vendor = slash > 0 ? `<span class="model-vendor">${esc(model.slice(0, slash + 1))}</span>` : "";
    btn.innerHTML = (modelProvider ? `<span class="model-prov">${esc(modelProvider)} · </span>` : "") + vendor + esc(model.slice(slash + 1) + (effort ? " · " + effort : ""));
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
  // A turn that finished (or stopped to ask) while the tab was in the
  // background marks the tab title until the user looks — no permission
  // prompt needed, unlike a desktop notification.
  let unseen = false;
  // tailPath keeps the end of a long path — the project, which is what
  // tells two windows apart — and drops the front: "…/dog/proj/py-bug".
  function tailPath(p) {
    if (p.length <= 48) return p;
    const parts = p.split("/").filter(Boolean);
    return parts.length > 3 ? "…/" + parts.slice(-3).join("/") : p;
  }
  function paintTitle() {
    const shown = cleanTitle(curTitle) || clipLine(firstUserText);
    document.getElementById("title").textContent = shown || "New conversation";
    document.title = (unseen ? "● " : "") + (shown ? shown + " — " : "") + "janetcode";
  }
  function markUnseen() {
    if (!document.hidden || unseen) return;
    unseen = true;
    paintTitle();
  }
  document.addEventListener("visibilitychange", () => {
    if (document.hidden || !unseen) return;
    unseen = false;
    paintTitle();
  });
  let pageSession = "";
  function applyHello(h) {
    if (!h) return;
    if (h.session) pageSession = h.session;
    // A different build than the page was served from means the server
    // restarted with a new binary; this JavaScript is stale no matter
    // how hard the user refreshes against the old process's memory.
    if (window.__janetUIBuild && h.version && window.__janetUIBuild !== h.version) {
      document.getElementById("reload-toast").hidden = false;
    }
    const wasConnected = connected;
    connected = h.connected !== false;
    modelLabel = [h.provider, h.model].filter(Boolean).join(" · ");
    modelProvider = h.provider && h.model ? h.provider : "";
    paintModel();
    setModeChip(h.mode);
    const cwdEl = document.getElementById("cwd");
    cwdEl.textContent = tailPath(h.cwd_short || h.cwd || "");
    cwdEl.title = h.cwd || "Working directory";
    if (h.title) curTitle = cleanTitle(h.title);
    paintTitle();
    const leaf = (h.cwd || "").replace(/\\/g, "/").split("/").filter(Boolean).pop();
    if (leaf) document.getElementById("empty-lead").textContent = "What should we change in " + leaf + "?";
    if ((h.cwd || "") !== suggestedFor) loadSuggestions(h.cwd || "");
    loadSessions();
    if (!connected && wasConnected) openConnect();
  }

  const reloadBtn = document.getElementById("reload-btn");
  if (reloadBtn) reloadBtn.addEventListener("click", () => location.reload());

  function applyStatus(st) {
    if (!st) return;
    const prev = running;
    running = !!st.running;
    if (prev && !running) announce(st.status || "Done");
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
    // A note the page wrote itself (the server is unreachable) is newer
    // than a repeat of the server's last status, and stays until the
    // server says something new.
    const said = bits.join(" · ");
    if (localNote && !st.running && said === lastServerStatus) return finishStatus(st);
    localNote = "";
    lastServerStatus = said;
    statusEl.textContent = said;
    finishStatus(st);
  }

  let localNote = "", lastServerStatus = "";
  function finishStatus(st) {
    statusEl.classList.toggle("busy", !!st.running);
    if (st.mode) setModeChip(st.mode);
    setEffortChip(st.effort);
    if (st.phase) lastFlavor = st.phase;
    relabelThinking();
    refreshWorkspace();
    trajectory.statusChanged(st);
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
    const names = { ask: "Ask first", AUTO: "Auto", EDITS: "Auto-edit", PLAN: "Plan", "READ-ONLY": "Read-only", YOLO: "Allow all" };
    chip.textContent = names[m] || m;
    chip.title = "Permission mode: " + (names[m] || m) + " — click to switch";
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
    // A bare "effort" read as a stray word, and "effort default" as
    // jargon; "auto" says the model decides.
    // The chip is the right half of the model pill: a gauge and the level.
    // Its name is read out whole.
    chip.innerHTML = icon("gauge") + `<span>${esc(effort || "auto")}</span>`;
    chip.setAttribute("aria-label", "Effort: " + (effort || "auto"));
    chip.title = effort ? "Reasoning effort " + effort + " — click to change" : "The model's own reasoning effort — click to set one";
    paintModel();
  }

  // announce says something once through the polite live region. The
  // text is cleared first so the same words twice are read twice.
  const srAnnounce = document.getElementById("sr-announce");
  function announce(text) {
    if (!srAnnounce || !text) return;
    srAnnounce.textContent = "";
    setTimeout(() => { srAnnounce.textContent = text; }, 30);
  }

  // askQuestion words the card as a question about the action rather
  // than the tool's internal name: "Edit mathx.go?", "Run go test ./...?"
  // instead of "Allow edit_file — mathx.go?". The target is set in code
  // so a command's own punctuation does not run into the question mark.
  const askVerbs = { edit_file: "Edit", write_file: "Write", bash: "Run", fetch_url: "Fetch", web_search: "Search the web for" };
  function askQuestion(p) {
    askLine.textContent = "";
    const verb = askVerbs[p.name];
    askLine.append(verb && p.summary ? verb + " " : "Allow " + p.name + (p.summary ? " — " : ""));
    if (p.summary) {
      const code = document.createElement("code");
      code.textContent = p.summary;
      askLine.append(code);
    }
    askLine.append("?");
    if (p.warning) {
      const w = document.createElement("span");
      w.className = "ask-warn";
      w.textContent = " Warning: " + p.warning;
      askLine.append(w);
    }
  }

  function showAsk(p) {
    const was = ask.hidden ? null : pending;
    pending = p && p.name ? p : null;
    ask.hidden = !pending;
    if (!pending) return;
    markUnseen();
    askQuestion(p);
    // A new request, not the same one sent again on a reconnect.
    const same = was && was.name === p.name && was.summary === p.summary && was.preview === p.preview;
    if (!same) {
      announce("Approval needed. " + askLine.textContent + (p.impact ? " " + p.impact : ""));
      notifyAsk(askLine.textContent);
      // Focus goes to the card, not to Allow: a stray Enter must not
      // approve a command. It stays put while the user is typing.
      const typing = document.activeElement === input && input.value.trim() !== "";
      if (!typing) ask.focus();
    }
    showRungs(p);
    // The rung picker already names the rule; a "rule:" line above it
    // said the same thing twice.
    askRule.hidden = !p.rule || !askRungs.hidden;
    if (!askRule.hidden) askRule.textContent = "rule: " + p.rule + (p.learned ? "  ·  " + p.learned : "");
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
        if (suggestedFor !== null) loadSuggestions(suggestedFor);
        curTitle = "";
        firstUserText = "";
        lastPrompt = "";
        break;
      case "notice":
        statusEl.textContent = msg.notice || "";
        break;
      case "connect": openConnect(); break;
    }
  }

  // post never throws: a server that is down (restarting, killed) made
  // fetch reject, the promise went unhandled and the caller never learned
  // the message had gone nowhere.
  async function post(path, body) {
    let r;
    try {
      r = await fetch(path, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body || {})
      });
    } catch (e) {
      statusEl.textContent = localNote = "Cannot reach janetcode. Is it still running?";
      return false;
    }
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
    if (!connected && !text.startsWith("/")) {
      // Keep what they typed: it sends once a provider is connected.
      openConnect("Connect a provider first. Your message is still in the composer.");
      return;
    }
    if (!text.startsWith("/")) lastPrompt = text;
    histPush(text);
    const draft = input.value, attached = chips.slice();
    input.value = "";
    chips.length = 0;
    paintChips();
    resize();
    acHide();
    hideSlash();
    if (!(await post("/api/prompt", { text }))) {
      // Not sent: put it back rather than let it vanish. post has already
      // said why in the status line.
      if (!input.value) input.value = draft;
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
        await post("/api/permission", { decision: map[ev.key.toLowerCase()], rule: chosenRule(), id: pending.id });
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
    await post("/api/permission", { decision: b.dataset.d, rule: chosenRule(), id: pending && pending.id });
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
  // pinned: the reader is on the tail, so anything that grows the
  // thread or shrinks the viewport (a highlighted block, a drawn
  // diagram, a taller composer, a drawer opening) keeps them there.
  // Only the reader's own scroll unpins.
  let pinned = true;
  scroller().addEventListener("scroll", () => {
    const el = scroller();
    pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 80;
    paintToLatest();
  }, { passive: true });
  const keepPinned = new ResizeObserver(() => {
    if (pinned) { const el = scroller(); el.scrollTop = el.scrollHeight; }
    paintToLatest();
    measureGutter();
  });
  keepPinned.observe(log);
  keepPinned.observe(scroller());
  // The scroller reserves a scrollbar gutter on its right; the dock
  // mirrors it so the composer centres exactly under the thread.
  const measureGutter = () => {
    const el = scroller();
    document.documentElement.style.setProperty("--sbw", (el.offsetWidth - el.clientWidth) + "px");
  };
  measureGutter();
  toLatest.addEventListener("click", () => {
    const el = scroller();
    el.scrollTo({ top: el.scrollHeight, behavior: "smooth" });
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

  // The /connect dialog lives in connect.js.
  const { open: openConnect } = JanetConnect({ closePalette, composer: input, status: statusEl });
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
  // uploadFiles is defined with the paste and drop handling further down;
  // a drop on the composer reaches the column's handler there.
  document.getElementById("attach-btn").addEventListener("click", () => attachInput.click());
  attachInput.addEventListener("change", () => { uploadFiles([...attachInput.files]); attachInput.value = ""; });
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
    const ledger = document.getElementById("traj-ledger");
    const thread = currentView === "trajectory" && ledger.scrollHeight > ledger.clientHeight ? ledger : scroller();
    if (ev.key === "j") thread.scrollBy({ top: 72, behavior: "smooth" });
    else if (ev.key === "k") thread.scrollBy({ top: -72, behavior: "smooth" });
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

  // contextSize is a window as people say it: "128k context", "1M context".
  function contextSize(n) {
    if (!n) return "";
    if (n >= 1e6) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M context";
    return Math.round(n / 1000) + "k context";
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
        // The model in use leads its group, so the menu opens on it
        // rather than on whatever sorts first ("aion-labs/…").
        const rows = g.models.filter((m) => !q || m.model.toLowerCase().includes(q))
          .sort((a, b) => (b.current ? 1 : 0) - (a.current ? 1 : 0));
        if (!rows.length) continue;
        html += `<div class="menu-group">${esc(g.label || g.provider)} · ${rows.length}</div>`;
        for (const m of rows) {
          switchRows.push({ cmd: "/model " + m.model });
          html += rowHTML(switchRows.length - 1, m.model + (m.current ? " ✓" : ""), contextSize(m.context));
        }
      }
      // A name nobody listed is still switchable: /model routes it by
      // family or prefix, and remembers it.
      if (q && !modelGroups.some((g) => g.models.some((m) => m.model.toLowerCase() === q))) {
        switchRows.unshift({ cmd: "/model " + q });
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
    if (!connected) { openConnect(); return; }
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
      if (!ws.todos || !ws.todos.length) todos.innerHTML = `<li class="quiet">No plan yet. When the model breaks a task into steps, they show here.</li>`;
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
      // Before the first message the percentage is only the system
      // prompt and tools, which read as something already used up.
      const talked = [...latest.values()].some((e) => e.kind === "user");
      if (ws.ctx_pct && talked) {
        chip.hidden = false;
        chip.innerHTML = `<span class="ring" style="--p:${Math.min(100, Math.max(0, ws.ctx_pct))}"></span>${ws.ctx_pct}%`;
        chip.title = "Context window " + ws.ctx_pct + "% used" + (ws.ctx_label ? " (" + ws.ctx_label + ")" : "");
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
      if (!label || label.startsWith("[") || label.startsWith("<")) label = clipLine(firstUserText) || "New conversation";
      curEl.innerHTML =
        `<div class="cur-row" title="The conversation open now">` +
        `<span class="cur-dot"></span><span class="cur-label">${esc(live.prefix || "")}${esc(label)}</span>` +
        `<span class="cur-live">open</span></div>`;
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
      (r.note || "").toLowerCase().includes(q) ||
      !!r.hit;
    const kept = rows.filter((r) => !r.current).filter(match).slice(0, 40);
    if (!kept.length) {
      ul.innerHTML = `<li class="quiet">${q ? "No conversation matches" : "Earlier conversations in this project will appear here."}</li>`;
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
        `<li class="${r.current ? "on" : ""}" data-id="${esc(r.id)}" title="${esc(r.note || "Resume this conversation")}" style="padding-left:${8 + (r.depth || 0) * 10}px"><span>${esc(r.prefix || "")}${esc(r.label)}</span><span class="when">${esc(relTime(r.updated))}${r.note ? " · parent elsewhere" : ""}</span>${r.hit ? `<span class="sess-hit">${r.turn ? "turn " + r.turn + ": " : ""}${esc(r.hit)}</span>` : ""}</li>`
      );
    }
    ul.innerHTML = parts.join("");
  }

  // The filter searches what the conversations said as well as their
  // titles: with three letters or more the server reads the sessions for
  // them, so that request waits for a pause in the typing.
  async function loadSessions() {
    try {
      const q = sessionFilter.value.trim();
      const url = q.length >= 3 ? "/api/sessions?q=" + encodeURIComponent(q) : "/api/sessions";
      const rows = await (await fetch(url)).json();
      renderSessions(rows, sessionFilter.value);
    } catch (_) {}
  }

  const sessionFilter = document.getElementById("session-filter");
  let sessionFilterTimer = 0;
  sessionFilter.addEventListener("input", () => {
    clearTimeout(sessionFilterTimer);
    sessionFilterTimer = setTimeout(loadSessions, 250);
  });

  document.getElementById("sessions").addEventListener("click", async (ev) => {
    const li = ev.target.closest("li[data-id]");
    if (!li || li.classList.contains("on")) return;
    // The server clears the page itself: it sends "reset" and then the
    // loaded thread on /events before it answers here. Clearing the log
    // again on this answer wiped a thread that had already arrived,
    // leaving the empty page over a conversation that was loaded.
    if (await post("/api/load", { id: li.dataset.id })) {
      if (window.matchMedia("(max-width: 960px)").matches && !shell.classList.contains("nav-collapsed")) {
        setCollapsed("nav-collapsed", true);
      }
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
    // As with a resumed conversation, the "reset" on /events clears
    // the page.
    if (await post("/api/new", {})) {
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
    terminal.restyle();
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
    if (b) b.setAttribute("aria-checked", soundOn ? "true" : "false");
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
    applyThemeChoice(document.documentElement.dataset.themeChoice || "system");
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
  ];
  const modeMenuEl = document.getElementById("mode-menu");
  const modeChip = document.getElementById("mode-chip");
  function hideModeMenu() { modeMenuEl.hidden = true; }
  function activeModeKeys() {
    const ws = lastWorkspace || {};
    return [ws.yolo && "YOLO", ws.auto && "AUTO", ws.edits && "EDITS", ws.plan && "PLAN", ws.readonly && "READ-ONLY"].filter(Boolean);
  }
  function paintModeMenu() {
    const on = activeModeKeys();
    modeMenuEl.innerHTML = MODE_ITEMS.map((it) => {
      const isOn = it.key === "ask" ? !on.length : on.includes(it.key);
      return `<div class="${isOn ? "on" : ""}" data-key="${it.key}"><span class="mk">✓</span><span class="name">${it.label}</span><span>${it.what}</span></div>`;
    }).join("");
  }
  modeChip.addEventListener("click", () => {
    if (!modeMenuEl.hidden) { hideModeMenu(); return; }
    paintModeMenu();
    modeMenuEl.hidden = false;
  });
  modeChip.addEventListener("keydown", (ev) => {
    if (ev.key === "Escape" && !modeMenuEl.hidden) { ev.preventDefault(); hideModeMenu(); }
  });
  modeMenuEl.addEventListener("click", async (ev) => {
    const row = ev.target.closest("[data-key]");
    if (!row) return;
    hideModeMenu();
    const item = MODE_ITEMS.find((it) => it.key === row.dataset.key);
    const cmds = item.cmd ? [item.cmd] : activeModeKeys().map((k) => MODE_ITEMS.find((it) => it.key === k).cmd);
    for (const c of cmds) await post("/api/command", { text: c });
  });
  document.addEventListener("click", (ev) => {
    if (!modeMenuEl.hidden && !ev.target.closest("#mode-menu") && !ev.target.closest("#mode-chip")) hideModeMenu();
  });
  document.addEventListener("keydown", (ev) => {
    if ((ev.ctrlKey || ev.metaKey) && ev.shiftKey && ev.key.toLowerCase() === "o") {
      ev.preventDefault();
      document.getElementById("new-chat").click();
    }
  });

  // The empty page's chips come from the project: its uncommitted work, a
  // TODO, its last commit. The ones in the page stay when the call fails.
  function loadSuggestions(cwd) {
    suggestedFor = cwd;
    fetch("/api/suggest").then((r) => (r.ok ? r.json() : null)).then((list) => {
      if (!Array.isArray(list) || !list.length || suggestedFor !== cwd) return;
      const box = document.querySelector("#empty .chips");
      box.replaceChildren(...list.map((sg) => {
        const b = document.createElement("button");
        b.type = "button";
        b.className = "chip";
        b.dataset.q = sg.prompt;
        b.textContent = sg.label;
        b.title = sg.prompt;
        return b;
      }));
    }).catch(() => {});
  }

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

  document.getElementById("view-ring").addEventListener("click", (ev) => {
    const btn = ev.target.closest("[data-view]");
    if (!btn) return;
    currentView = btn.dataset.view;
    document.querySelectorAll("#view-ring [data-view]").forEach((b) => b.classList.toggle("on", b === btn));
    chatView.hidden = currentView !== "chat";
    trajView.hidden = currentView !== "trajectory";
    document.getElementById("thread").hidden = currentView === "terminal";
    document.querySelector(".col").classList.toggle("show-terminal", currentView === "terminal");
    if (currentView === "trajectory") trajectory.refresh();
    if (currentView === "terminal") terminal.show();
    else terminal.hide();
  });

  // The terminal tab lives in terminal.js, the trajectory panel in
  // trajectory.js.
  const terminal = JanetTerminal(document.getElementById("terminal"));
  const trajectory = JanetTrajectory({ isOpen: () => currentView === "trajectory" });

  const navToggle = document.getElementById("nav-toggle");
  const workToggle = document.getElementById("work-toggle");
  const narrow = window.matchMedia("(max-width: 960px)");
  function setCollapsed(cls, on) {
    shell.classList.toggle(cls, on);
    // On a narrow screen both rails are transient overlays: opening one
    // closes the other rather than stacking two drawers, and neither
    // says anything about the desktop layout.
    if (narrow.matches) {
      if (!on) shell.classList.add(cls === "nav-collapsed" ? "work-collapsed" : "nav-collapsed");
      return;
    }
    localStorage.setItem("janetcode-" + cls, on ? "1" : "");
  }
  shell.addEventListener("click", (ev) => {
    if (ev.target === shell && narrow.matches) {
      shell.classList.add("nav-collapsed");
      shell.classList.add("work-collapsed");
    }
  });
  narrow.addEventListener("change", () => {
    shell.classList.toggle("nav-collapsed", narrow.matches || localStorage.getItem("janetcode-nav-collapsed") === "1");
    shell.classList.toggle("work-collapsed", narrow.matches || localStorage.getItem("janetcode-work-collapsed") !== "");
  });
  navToggle.addEventListener("click", () => setCollapsed("nav-collapsed", !shell.classList.contains("nav-collapsed")));
  workToggle.addEventListener("click", () => setCollapsed("work-collapsed", !shell.classList.contains("work-collapsed")));
  // The work rail starts closed. The session rail starts open, and only
  // stays shut when this browser was told to shut it.
  if (localStorage.getItem("janetcode-nav-collapsed") === "1" || narrow.matches) shell.classList.add("nav-collapsed");
  // A panel left open on a desktop does not reopen over a phone's thread.
  if (localStorage.getItem("janetcode-work-collapsed") !== "" || narrow.matches) shell.classList.add("work-collapsed");
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
    if (ev.target.closest(".fix-btn")) {
      openConnect();
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
    markUnseen();
    if (soundOn) ding();
    if (!notifyOn) return;
    const title = document.getElementById("title").textContent || "janetcode";
    const body = lastReply ? lastReply.split("\n")[0].slice(0, 120) : (st.status || "Turn finished");
    try { new Notification("janetcode · " + title, { body }); } catch (_) {}
  }

  // A turn waiting on an approval in a tab you are not looking at looks
  // idle; the terminal UI rings for it, and so does the page. One tag, so
  // a second request replaces the first instead of stacking.
  function notifyAsk(line) {
    if (!document.hidden) return;
    if (soundOn) ding();
    if (!notifyOn) return;
    try { new Notification("janetcode · approval needed", { body: line, tag: "janetcode-ask" }); } catch (_) {}
  }

  const conn = document.getElementById("conn");
  const es = new EventSource("/events");
  let lost = false;
  es.onopen = () => {
    conn.classList.remove("off"); conn.textContent = ""; conn.title = "Connected";
    if (!lost) return;
    lost = false;
    // Back after a gap. If the server restarted it opened a different
    // session, and the page went on showing the old conversation as if
    // it were live; say so and offer the reload that shows the new one.
    fetch("/api/state").then((r) => r.json()).then((st) => {
      const now = st && st.hello && st.hello.session;
      if (pageSession && now && now !== pageSession) {
        document.getElementById("reload-msg").textContent =
          "janetcode restarted in a new session. Reload to continue; this conversation is in the list on the left.";
        document.getElementById("reload-toast").hidden = false;
      }
    }).catch(() => {});
  };
  es.onerror = () => {
    lost = true; conn.classList.add("off"); conn.textContent = "Reconnecting"; conn.title = "Connection lost, retrying";
    // A server bound off loopback restarted with a new access token: the
    // cookie is stale and every retry would be refused. Reloading shows
    // the page that asks for the new one.
    fetch("/api/state").then((r) => { if (r.status === 401) location.reload(); }).catch(() => {});
  };
  es.onmessage = (ev) => { try { handle(JSON.parse(ev.data)); } catch (_) {} };
})();
