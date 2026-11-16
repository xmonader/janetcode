(() => {
  const S = window.JanetState; // the shared view model (state.js)
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
  const openTools = new Set();
  const openThoughts = new Set();
  const openAnswers = new Set();
  const openRuns = new Set();
  const openFileCards = new Set();
  let running = false;
  let commands = [];
  let palIdx = 0;
  let palItems = [];
  let lastUserText = "";
  // The conversation's first prompt names it until the server titles it,
  // as the rail does; the latest prompt renamed the header every turn.
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

  // Consecutive tool calls live in one collapsible card (toolgroups.js).
  // toolIsOpen is the one place that says whether a call's body shows:
  // edits open by default, the reader's click flips it.
  function toolIsOpen(e) {
    const k = toolKind(e.name || "").k;
    let byDefault = (k === "edit" || k === "write") && !!e.preview && !e.denied;
    // Settings > Tool calls (extras.js): all open or all collapsed.
    const pref = window.JanetExtras && window.JanetExtras.toolDefault ? window.JanetExtras.toolDefault() : "auto";
    if (pref === "open") byDefault = !!(e.preview || e.output) && !e.denied;
    else if (pref === "collapsed") byDefault = false;
    return byDefault !== openTools.has(e.id);
  }
  function toolSetOpen(e, want) {
    if (toolIsOpen(e) === want) return;
    if (openTools.has(e.id)) openTools.delete(e.id); else openTools.add(e.id);
    render(S.latest.get(e.id));
  }
  const groups = JanetToolGroups({ log, entry: (id) => S.latest.get(id), isOpen: toolIsOpen, setOpen: toolSetOpen, icon });

  let lastCreated = 0;
  function render(e) {
    if (e.gone) {
      // The server dropped it from the thread (an empty thinking
      // placeholder, discarded text): drop the row too.
      const g = nodes.get(e.id);
      if (g) { groups.release(g); g.remove(); }
      nodes.delete(e.id);
      S.latest.delete(e.id);
      return;
    }
    S.latest.set(e.id, e);
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
      groups.place(n, e);
      nodes.set(e.id, n);
    }
    if (e.at) n.title = new Date(e.at).toLocaleString();
    if (e.kind === "user") {
      n.className = "turn you";
      lastUserText = e.text || "";
      if (!S.firstUserText) S.firstUserText = lastUserText;
      if (!curTitle) paintTitle();
      loadSessions();
      n.innerHTML = `<div class="bubble">${esc(e.text || "")}</div><div class="msg-actions">` +
        `<button type="button" class="act copy-btn" data-copy="${e.id}" title="Copy" aria-label="Copy message">${icon("copy")}</button>` +
        `<button type="button" class="act" data-edit="${e.id}" title="Edit & resend" aria-label="Edit and resend">${icon("pencil")}</button>` +
        `<button type="button" class="act" data-branch="${e.id}" title="Branch here" aria-label="Branch the conversation from this message">${icon("branch")}</button></div>`;
    } else if (e.kind === "assistant") {
      n.className = "turn";
      const copyBtn = `<button type="button" class="act copy-btn" data-copy="${e.id}" title="Copy as Markdown" aria-label="Copy reply as Markdown">${icon("copy")}</button><button type="button" class="act" data-copyturn="${e.id}" title="Copy the whole turn as Markdown" aria-label="Copy the whole turn as Markdown">${icon("file")}</button>`;
      const regenBtn = lastUserText ? `<button type="button" class="act" data-regen="1" title="Ask again" aria-label="Ask again">${icon("refresh")}</button>` : "";
      const meta = [];
      if (e.at) meta.push(new Date(e.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }));
      if (e.model) meta.push(e.model);
      if (e.tok_in || e.tok_out) meta.push("↑" + fmtTok(e.tok_in) + " ↓" + fmtTok(e.tok_out));
      if (e.ms) meta.push(fmtDur(e.ms));
      const metaRow = e.done ? `<div class="msg-foot"><span class="msg-actions">${copyBtn}${regenBtn}<button type="button" class="act" data-branch="${e.id}" title="Branch here" aria-label="Branch the conversation from this reply">${icon("branch")}</button></span><span class="msg-meta">${esc(meta.join(" · "))}</span></div>` : "";
      const long = e.done && (e.text || "").length > 1800;
      const clipped = long && !openAnswers.has(e.id);
      const more = clipped ? `<button type="button" class="continue" data-more="${e.id}">Show full reply</button>` : "";
      n.innerHTML = `<div class="md${clipped ? " clip" : ""}">${JanetMD.render(e.text || "", e.done)}${e.done ? "" : '<span class="caret">▍</span>'}</div>${more}${metaRow}${e.done ? linkCards(e.text) : ""}`;
      if (e.done) {
        S.lastReply = (e.text || "").slice(0, 200);
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
      const open = toolIsOpen(e);
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
        render(S.latest.get(e.id));
      };
      groups.refresh(n);
      if (S.pending) paintAskQueue();
    } else if (e.kind === "perf") {
      n.className = "turn card";
      n.innerHTML = perfCardHTML(e.data);
    } else if (e.kind === "usage") {
      n.className = "turn card";
      n.innerHTML = usageCardHTML(e.data);
    } else if (e.kind === "checkpoints") {
      n.className = "turn card";
      n.innerHTML = checkpointsCardHTML(e.data || []);
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
    } else if (e.kind === "page") {
      JanetPages.build(n, e, icon);
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
    // A card's own command buttons (the checkpoints card's preview and
    // apply) run the command they name, as typing it would.
    const cmdBtn = ev.target.closest("button[data-cmd]");
    if (cmdBtn) {
      await post("/api/command", { text: cmdBtn.dataset.cmd });
      return;
    }
    const wrap = ev.target.closest("[data-wrap]");
    if (wrap) {
      const pre = wrap.closest(".codeblock")?.querySelector("pre");
      if (pre) pre.style.whiteSpace = pre.style.whiteSpace === "pre-wrap" ? "pre" : "pre-wrap";
      return;
    }
    const more = ev.target.closest("[data-more]");
    if (more) {
      openAnswers.add(more.dataset.more);
      render(S.latest.get(more.dataset.more));
      return;
    }
    const runBtn = ev.target.closest("[data-run]");
    if (runBtn) {
      const id = runBtn.dataset.run;
      if (openRuns.has(id)) openRuns.delete(id); else openRuns.add(id);
      S.latest.forEach((e) => { if (e.run === id) render(e); });
      return;
    }
    const thought = ev.target.closest("[data-thought]");
    if (thought && S.latest.get(thought.dataset.thought)?.done) {
      const id = thought.dataset.thought;
      if (openThoughts.has(id)) openThoughts.delete(id); else openThoughts.add(id);
      render(S.latest.get(id));
      return;
    }
    const filesMore = ev.target.closest("[data-files]");
    if (filesMore) {
      const t = filesMore.dataset.files;
      if (openFileCards.has(t)) openFileCards.delete(t); else openFileCards.add(t);
      if (S.lastWorkspace) paintTurnFiles(S.lastWorkspace.files);
      return;
    }
    const fileLi = ev.target.closest(".turn-files li[data-seq]");
    if (fileLi) {
      const data = await (await fetch("/api/change?n=" + fileLi.dataset.seq)).json();
      showDetail(data.path || "", data.diff || "", true);
    }
  });

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
  let modelProvider = "";
  function paintModel() {
    const effort = document.getElementById("effort-chip")?.dataset.effort || "";
    const btn = document.getElementById("model");
    // The green dot says the page reaches janetcode; with no model behind
    // it, green next to "Not connected" read as the opposite.
    document.getElementById("conn")?.classList.toggle("idle", !S.connected);
    if (!S.connected) {
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
    const shown = cleanTitle(curTitle) || clipLine(S.firstUserText);
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
    const prevSession = pageSession;
    if (h.session) pageSession = h.session;
    if (prevSession && h.session && prevSession !== h.session) {
      // Another conversation: its own draft, not the last one's.
      input.value = "";
      restoreDraft();
      resize();
    }
    // A different build than the page was served from means the server
    // restarted with a new binary; this JavaScript is stale no matter
    // how hard the user refreshes against the old process's memory.
    if (window.__janetUIBuild && h.version && window.__janetUIBuild !== h.version) {
      document.getElementById("reload-toast").hidden = false;
    }
    const wasConnected = S.connected;
    S.connected = h.connected !== false;
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
    if (!S.connected && wasConnected) openConnect();
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
    if (S.localNote && !st.running && said === lastServerStatus) return finishStatus(st);
    S.localNote = "";
    lastServerStatus = said;
    statusEl.textContent = said;
    finishStatus(st);
  }

  let lastServerStatus = "";
  function finishStatus(st) {
    statusEl.classList.toggle("busy", !!st.running);
    if (st.mode) setModeChip(st.mode, st.dry_run);
    setEffortChip(st.effort);
    if (st.phase) lastFlavor = st.phase;
    relabelThinking();
    refreshWorkspace();
    trajectory.statusChanged(st);
  }

  function relabelThinking() {
    if (!lastFlavor) return;
    S.latest.forEach((e, id) => {
      if (e.kind !== "thinking" || e.done || (e.text || "").trim()) return;
      const n = nodes.get(id);
      if (n) n.innerHTML = `<span class="lbl">${esc(lastFlavor)}</span>`;
    });
  }

  // dry says the session is a dry run: not a permission mode of its own
  // but a layer over whichever is set, so the chip names both.
  function setModeChip(mode, dry) {
    const chip = document.getElementById("mode-chip");
    if (!chip) return;
    const m = mode || "ask";
    const names = { ask: "Ask first", AUTO: "Auto", EDITS: "Auto-edit", PLAN: "Plan", "READ-ONLY": "Read-only", YOLO: "Allow all", AWAY: "Away" };
    chip.textContent = (names[m] || m) + (dry ? " · dry run" : "");
    chip.title = "Permission mode: " + (names[m] || m) + (dry ? " (dry run: nothing reaches the disk until /dryrun apply)" : "") + " — click to switch";
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
    chip.innerHTML = icon("gauge") + `<span><span class="eff-l">effort: </span>${esc(effort || "auto")}</span>`;
    chip.setAttribute("aria-label", "Effort: " + (effort || "auto"));
    chip.title = effort ? "Reasoning effort: " + effort + " — click to change" : "Reasoning effort: the model decides — click to set one";
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

  // The approval card, questions and the secret card live in approvals.js.
  const { showAsk, showQuestions, showSecret, paintAskQueue, chosenRule } = JanetApprovals({
    S, esc, post, diffHTML, toolKind, markUnseen, announce, input, statusEl,
    notifyAsk: (line) => notifyAsk(line) });

  // A prompt link (?prompt=<text>) opens the page with the text in the
  // composer as a draft, and nothing else: it is never sent, and it
  // touches no mode, model or session setting (no other parameter is
  // read). It is assigned to .value, so it is text, never markup. It is
  // cut at PROMPT_LINK_MAX characters, and an existing draft is kept
  // rather than replaced, since the link has no business discarding
  // what you typed.
  const PROMPT_LINK_MAX = 8000;
  let linkPrompt = "";
  let linkPromptCut = false;
  try {
    const qs = new URLSearchParams(location.search);
    if (qs.has("prompt")) {
      // Control characters other than tab and newline have no place in a
      // draft (a link can carry NUL, escapes, bidi marks).
      let t = qs.get("prompt").replace(/[\u0000-\u0008\u000b-\u001f\u007f‪-‮⁦-⁩]/g, "");
      if (t.length > PROMPT_LINK_MAX) { t = t.slice(0, PROMPT_LINK_MAX); linkPromptCut = true; }
      linkPrompt = t;
      qs.delete("prompt");
      const rest = qs.toString();
      history.replaceState(null, "", location.pathname + (rest ? "?" + rest : "") + location.hash);
    }
  } catch (_) {}
  function applyPromptLink() {
    if (!linkPrompt.trim()) return;
    const t = linkPrompt;
    linkPrompt = "";
    if (input.value.trim()) {
      linkNote("A link offered a prompt, but the composer already has a draft: it was not replaced.");
      return;
    }
    input.value = t;
    saveDraft();
    resize();
    if (!S.pending) {
      input.focus();
      input.setSelectionRange(t.length, t.length);
    }
    linkNote("Prompt from a link, not sent: read it before you press Enter." + (linkPromptCut ? " It was cut at " + PROMPT_LINK_MAX + " characters." : ""));
  }
  // The note sits above the composer until you type or send; the status
  // line is rewritten by every status event and would lose it.
  const linkNoteEl = document.getElementById("link-note");
  function linkNote(text) {
    linkNoteEl.textContent = text;
    linkNoteEl.hidden = !text;
  }
  input.addEventListener("input", () => linkNote(""));

  function handle(msg) {
    switch (msg.type) {
      case "hello": applyHello(msg.hello); break;
      case "entry": if (msg.entry) render(msg.entry); break;
      case "status": applyStatus(msg.status); break;
      case "permission": showAsk(msg.permission); break;
      case "guest": applyGuest(msg.guest); break;
      case "question": showQuestions(msg.questions); break;
      case "secret": showSecret(msg.secret); break;
      case "reset":
        log.innerHTML = "";
        nodes.clear();
        S.latest.clear();
        openTools.clear();
        openThoughts.clear();
        openAnswers.clear();
        openRuns.clear();
        empty.hidden = false;
        if (suggestedFor !== null) loadSuggestions(suggestedFor);
        curTitle = "";
        S.firstUserText = "";
        lastPrompt = "";
        break;
      case "notice":
        statusEl.textContent = msg.notice || "";
        break;
      case "connect": openConnect(); break;
      case "compose":
        // A /commit or /pr draft: into the composer only when it is empty,
        // so nothing the user was typing is lost.
        if (!input.value.trim() && msg.compose) {
          input.value = msg.compose;
          input.dispatchEvent(new Event("input"));
          input.focus();
        }
        break;
    }
  }

  // post never throws: a server that is down (restarting, killed) made
  // fetch reject, the promise went unhandled and the caller never learned
  // the message had gone nowhere.
  // checkpointsCardHTML draws /checkpoints: newest first, each restore
  // point with what it covers and a button that previews restoring the
  // tree to before that turn; Apply carries out the last preview.
  function checkpointsCardHTML(rows) {
    const items = rows.map((c) =>
      `<li><button type="button" class="act" data-cmd="/undo turn ${c.turn}" title="Preview restoring to before turn ${c.turn}">Preview</button> ` +
      `<code>${esc(c.id)}</code> ${esc(c.what)} · <span class="dim">${esc(c.coverage)}</span>` +
      (c.complete ? "" : ` <span class="warn">incomplete: ${esc((c.gaps || []).join("; "))}</span>`) + `</li>`).join("");
    return `<div class="k">checkpoints</div><ul class="checkpoints">${items}</ul>` +
      `<div><button type="button" class="act" data-cmd="/undo apply">Apply the last preview</button></div>`;
  }

  // Guest links: the bar shows an active link and the messages a guest
  // left, each a Request the owner sends as a turn or dismisses. The text
  // is guest-written, so it only ever goes in with textContent.
  const guestBar = document.getElementById("guest-bar");
  const guestReqs = document.getElementById("guest-reqs");
  const shareBtn = document.getElementById("share-btn");
  let guestPath = "";
  function applyGuest(g) {
    g = g || {};
    const reqs = g.requests || [];
    guestPath = g.active ? g.path || "" : "";
    guestBar.hidden = !g.active && reqs.length === 0;
    shareBtn.setAttribute("aria-pressed", g.active ? "true" : "false");
    shareBtn.classList.toggle("on", !!g.active);
    const state = document.getElementById("guest-state");
    state.textContent = g.active
      ? "Sharing with a guest · read-only · ends " + new Date(g.expires).toLocaleString([], { weekday: "short", hour: "2-digit", minute: "2-digit" })
      : "Link ended";
    document.getElementById("guest-copy").hidden = !g.active;
    guestReqs.replaceChildren(...reqs.map((r) => {
      const li = document.createElement("li");
      const tag = document.createElement("span");
      tag.className = "k";
      tag.textContent = "Request";
      const txt = document.createElement("span");
      txt.className = "guest-text";
      txt.textContent = r.text;
      const mk = (label, action, cls) => {
        const b = document.createElement("button");
        b.type = "button";
        b.className = cls;
        b.textContent = label;
        b.addEventListener("click", () => post("/api/guest", { action, id: r.id }));
        return b;
      };
      li.append(tag, txt, mk("Send", "send", "act primary"), mk("Dismiss", "dismiss", "act"));
      return li;
    }));
  }
  async function copyGuestLink() {
    if (!guestPath) return;
    const url = location.origin + guestPath;
    try {
      await navigator.clipboard.writeText(url);
      statusEl.textContent = "Guest link copied. It is read-only and ends in 24 hours or when you stop sharing.";
    } catch (_) {
      statusEl.textContent = "Guest link: " + url;
    }
  }
  async function shareWithGuest() {
    if (!guestPath) {
      const r = await fetch("/api/guest", { method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ action: "create" }) }).catch(() => null);
      if (!r || !r.ok) { statusEl.textContent = r ? (await r.text()).trim() : "Cannot reach janetcode."; return; }
      const out = await r.json();
      guestPath = new URL(out.url).pathname;
    }
    await copyGuestLink();
  }
  shareBtn.addEventListener("click", shareWithGuest);
  document.getElementById("guest-copy").addEventListener("click", copyGuestLink);
  document.getElementById("guest-stop").addEventListener("click", () => post("/api/guest", { action: "stop" }));

  async function post(path, body) {
    let r;
    try {
      r = await fetch(path, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(body || {})
      });
    } catch (e) {
      statusEl.textContent = S.localNote = "Cannot reach janetcode. Is it still running?";
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
    if (!text.startsWith("/")) lastPrompt = text;
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
  const draftKey = () => LEGACY_DRAFT_KEY + (pageSession ? ":" + pageSession : "");
  function saveDraft() {
    try {
      if (input.value) localStorage.setItem(draftKey(), input.value);
      else localStorage.removeItem(draftKey());
    } catch (_) {}
  }
  function restoreDraft() {
    try {
      let d = localStorage.getItem(draftKey());
      if (!d && pageSession) {
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
    if (S.pending && input.value === "" && !ev.metaKey && !ev.ctrlKey) {
      const map = { y: "allow", a: "session", r: "always", n: "deny", c: "cancel" };
      if (map[ev.key.toLowerCase()]) {
        ev.preventDefault();
        await post("/api/permission", { decision: map[ev.key.toLowerCase()], rule: chosenRule(), id: S.pending.id });
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

  // The find bar lives in find.js.
  const { open: openFind } = JanetFind({ scroller, input });

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
  // The palette merges everything the page can do: actions, views, the
  // workbench tabs, settings, and the server's slash commands. Each item
  // carries its own run(); a slash command that takes arguments fills the
  // composer instead of running.
  function paletteActions() {
    const act = (group, name, help, run, key) => ({ group, name, help, run, key });
    const mdFor = (c) => act("Commands", c.name, c.help || "", async () => {
      if (c.args) { input.value = c.name + " "; resize(); input.focus(); } else await post("/api/command", { text: c.name });
    });
    const tab = (pane, label) => act("Workbench", label, "open the " + label.toLowerCase() + " tab", () => openWorkTab(pane));
    return [
      act("Actions", "New session", "start a fresh conversation", () => document.getElementById("new-chat").click(), "Ctrl Shift O"),
      act("Actions", "Find in conversation", "search the thread", () => openFind(), "Ctrl F"),
      act("Actions", "Export conversation", "save the chat as Markdown", exportChat),
      act("Actions", "Copy conversation", "copy the chat as Markdown", async () => { try { await navigator.clipboard.writeText(chatMarkdown()); statusEl.textContent = "Conversation copied as Markdown"; } catch (_) {} }),
      act("Actions", "Share with guest", "copy a read-only guest link for this session", shareWithGuest),
      act("Actions", "Stop sharing", "end the guest link now", () => post("/api/guest", { action: "stop" })),
      act("Actions", "Switch model", "pick another model", () => (S.connected ? openModelMenu() : openConnect())),
      act("Actions", "Set effort", "reasoning tier", () => openEffortMenu()),
      act("Actions", "Permission mode", "ask, auto, plan, read-only…", () => { modeMenu.paint(); modeMenu.el.hidden = false; }),
      act("Actions", "Connect a provider", "add or change an API key", () => openConnect()),
      act("Actions", "Keyboard shortcuts", "the help sheet", () => openHelp(), "?"),
      act("Actions", "Toggle sidebar", "conversations rail", () => navToggle.click()),
      act("Actions", "Toggle workbench", "plan, changes, jobs…", () => workToggle.click()),
      act("Actions", "Toggle web preview", "preview a running app", () => pvToggle.click()),
      act("Views", "Chat", "the conversation", () => setView("chat")),
      act("Views", "Trajectory", "the session as a timeline", () => setView("trajectory")),
      act("Views", "Terminal", "a shell in the project", () => setView("terminal")),
      tab("plan", "Plan"), tab("files", "Changes"), tab("jobs", "Jobs"), tab("agents", "Agents"), tab("ptc", "Scripts"), tab("session", "Session"),
      act("Settings", "Theme: light", "", () => applyThemeChoice("light")),
      act("Settings", "Theme: dark", "", () => applyThemeChoice("dark")),
      act("Settings", "Theme: system", "", () => applyThemeChoice("system")),
      act("Settings", "Density: comfortable", "", () => setDensity("comfortable")),
      act("Settings", "Density: compact", "tighter rows", () => setDensity("compact")),
      act("Settings", "Font size: small", "", () => setFontScale("small")),
      act("Settings", "Font size: medium", "", () => setFontScale("medium")),
      act("Settings", "Font size: large", "", () => setFontScale("large")),
      act("Settings", "Open settings", "appearance, sound, notifications", () => openSettings()),
    ].concat(window.JanetExtras && window.JanetExtras.actions ? window.JanetExtras.actions(act) : [], commands.map(mdFor));
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
  document.addEventListener("keydown", (ev) => {
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    const el = document.activeElement;
    const inField = el && (el.tagName === "TEXTAREA" || el.tagName === "INPUT" || el.isContentEditable || el.tagName === "SELECT");
    if (ev.key === "/" && !inField && currentView === "chat" && palette.hidden && helpEl.hidden && settingsEl.hidden) {
      ev.preventDefault();
      input.focus();
      return;
    }
    if (ev.key === "Escape" && !inField) {
      const viewer = document.getElementById("viewer");
      if (!viewer.hidden) { ev.preventDefault(); viewer.hidden = true; return; }
      if (!switchMenu.el.hidden) { ev.preventDefault(); closeSwitchMenu(); return; }
      if (!modeMenu.el.hidden) { ev.preventDefault(); modeMenu.hide(); return; }
      if (!palette.hidden) return;
      if (narrow.matches) { shell.classList.add("nav-collapsed", "work-collapsed"); }
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

  // The model/effort switch menu lives in switchmenu.js.
  const switchMenu = JanetSwitchMenu({ S, esc, post, openConnect });
  const { openModelMenu, openEffortMenu, closeSwitchMenu } = switchMenu;

  // The workspace pane and /api/workspace refresh live in workspacepane.js.
  const { refreshWorkspace, showDetail } = JanetWorkspacePane({ S, esc, diffHTML, post, setModeChip, paintGit, paintTurnFiles });

  // The sessions rail lives in sessionsrail.js.
  const { loadSessions } = JanetSessionsRail({ S, esc, icon, post, cleanTitle, clipLine, statusEl, input, refreshWorkspace,
    shell: document.querySelector(".shell"), setCollapsed: (...a) => setCollapsed(...a) });

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
  // entryMarkdown is one entry as the Markdown a reader would paste: the
  // prompt, the reply, a tool call with its output. Thinking and cards
  // are left out, as they are in the saved file.
  function entryMarkdown(e, o) {
    if (!e) return "";
    if (o) {
      if (e.kind === "thinking") return o.reasoning && (e.text || "").trim() ? "> **Reasoning**\n>\n" + e.text.trim().split("\n").map((l) => "> " + l).join("\n") : "";
      if (e.kind === "tool" && !o.tools) return "";
      if (e.kind === "tool" && !o.output) {
        return "- `" + (e.name || "tool") + "`" + (e.summary ? " " + e.summary.replace(/`/g, "'") : "") + (e.denied ? " (declined)" : e.error ? " (failed)" : "");
      }
    }
    if (e.kind === "user") return "## you\n\n" + (e.text || "");
    if (e.kind === "assistant") return "## janetcode\n\n" + (e.text || "");
    if (e.kind === "tool") {
      const head = "### " + (e.name || "tool") + (e.summary ? " `" + e.summary.replace(/`/g, "'") + "`" : "") + (e.denied ? " (declined)" : e.error ? " (failed)" : "");
      if (e.preview) return head + "\n\n```diff\n" + e.preview + "\n```";
      if (e.output) return head + "\n\n```\n" + e.output + "\n```";
      return head;
    }
    return "";
  }
  // turnMarkdown is the exchange an assistant reply belongs to: from the
  // prompt before it through every tool call to the reply itself.
  function turnMarkdown(id) {
    const arts = [...log.querySelectorAll("article.turn")];
    let i = arts.findIndex((a) => a.dataset.id === id);
    if (i < 0) return "";
    let from = i;
    while (from > 0 && !arts[from].classList.contains("you")) from--;
    return arts.slice(from, i + 1).map((a) => entryMarkdown(S.latest.get(a.dataset.id))).filter(Boolean).join("\n\n");
  }
  function chatMarkdown(o) {
    return [...log.querySelectorAll("article.turn")].map((a) => entryMarkdown(S.latest.get(a.dataset.id), o)).filter(Boolean).join("\n\n");
  }
  function exportChat() {
    // extras.js asks what to include first; without it, save everything as before.
    if (window.JanetExtras && window.JanetExtras.openExport) { window.JanetExtras.openExport(); return; }
    downloadMarkdown(chatMarkdown());
  }
  function downloadMarkdown(md) {
    if (!md) return;
    const blob = new Blob([md + "\n"], { type: "text/markdown" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = (cleanTitle(curTitle) || "janetcode").replace(/[\\/:*?"<>|]+/g, "-") + ".md";
    a.click();
    URL.revokeObjectURL(a.href);
  }
  document.getElementById("export-btn").addEventListener("click", exportChat);

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
    showSecret(st.secret);
    applyGuest(st.guest);
    refreshWorkspace();
    restoreDraft();
    applyPromptLink();
  }).catch((err) => { console.error("BOOT FAILED", err); });
  const shell = document.querySelector(".shell");
  // Theme, settings dialog and the ding live in settings.js.
  const settings = JanetSettings({ icon, terminal: () => terminal });
  const { applyThemeChoice, openSettings, setFontScale, setDensity, ding, quiet } = settings;
  const settingsEl = settings.el;
  // The permission-mode chip and menu live in modemenu.js.
  const modeMenu = JanetModeMenu({ S, post });
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

  function setView(name) {
    const btn = document.querySelector('#view-ring [data-view="' + name + '"]');
    if (btn) btn.click();
  }
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

  // The rails, workbench tabs, preview drawer and drag-resize live in panels.js.
  const { navToggle, workToggle, pvToggle, pvOpen, pvLoad, setCollapsed, openWorkTab, narrow } = JanetPanels({ shell });
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
      const src = S.latest.get(editBtn.dataset.edit);
      // A true rewind: the server cuts the history back to before this
      // message. Without sessions_ui.js it only refills the composer.
      if (src && window.sessionsUI && (await window.sessionsUI.edit(editBtn.dataset.edit, src))) return;
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
    const copyTurn = ev.target.closest("[data-copyturn]");
    if (copyTurn) {
      try {
        await navigator.clipboard.writeText(turnMarkdown(copyTurn.dataset.copyturn));
        copyTurn.innerHTML = icon("check");
        setTimeout(() => { copyTurn.innerHTML = icon("file"); }, 1500);
      } catch (_) {}
      return;
    }
    const copy = ev.target.closest(".copy-btn");
    if (copy) {
      const src = S.latest.get(copy.dataset.copy);
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

  async function uploadFiles(files) {
    if (!files.length) return;
    const fd = new FormData();
    files.forEach((f) => fd.append("files", f, f.name || "pasted-image.png"));
    statusEl.textContent = "Uploading " + files.length + " file" + (files.length > 1 ? "s" : "") + "…";
    try {
      const r = await fetch("/api/upload", { method: "POST", body: fd });
      const data = await r.json();
      if (!r.ok) { statusEl.textContent = typeof data === "string" ? data : r.statusText; return; }
      try { if (window.JanetExtras) window.JanetExtras.uploaded(files, data.paths || []); } catch (_) {}
      const refs = (data.paths || []).map(mentionOf).join(" ");
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

  // Desktop notifications live in notify.js.
  const { maybeNotify, notifyAsk } = JanetNotify({ S, settings, markUnseen });

  // The small surface extras.js (quick prompts, previews, gallery, export,
  // settings) reaches the page through.
  window.JanetApp = {
    entries: () => [...log.querySelectorAll("article.turn")].map((a) => S.latest.get(a.dataset.id)).filter(Boolean),
    chatMarkdown, downloadMarkdown, openFind, openConnect, openHelp, openPalette,
    setComposer: (t) => { input.value = t; resize(); input.dispatchEvent(new Event("input")); input.focus(); },
    insertComposer: (t) => {
      const a = input.selectionStart ?? input.value.length, b = input.selectionEnd ?? a;
      input.value = input.value.slice(0, a) + t + input.value.slice(b);
      input.selectionStart = input.selectionEnd = a + t.length;
      resize(); input.dispatchEvent(new Event("input")); input.focus();
    },
    rerenderTools: () => { openTools.clear(); S.latest.forEach((e) => { if (e.kind === "tool") render(e); }); },
    session: () => pageSession,
    title: () => cleanTitle(curTitle),
  };
  if (window.JanetExtras && window.JanetExtras.ready) window.JanetExtras.ready();

  const conn = document.getElementById("conn");
  const es = new EventSource("/events");
  window.__es = es;
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
