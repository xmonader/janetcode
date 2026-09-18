(() => {
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
  let running = false;
  let pending = null;
  let commands = [];
  let palIdx = 0;
  let palItems = [];
  let lastReply = "";
  let lastFlavor = "";

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }
  // Code blocks keep their raw source here, keyed by a generated id on
  // the Copy button — attribute-escaping megabytes of test output onto a
  // DOM node is slower and leakier than a lookup.
  const codeBlocks = new Map();
  let codeSeq = 0;

  // highlight is a deliberately small tokenizer: comments, strings,
  // numbers and keywords, colored generically so one pass serves every
  // language the model writes. Everything it emits is escaped first; the
  // spans are markup, never source.
  function highlight(src, lang) {
    const comments = { js: "//", ts: "//", javascript: "//", typescript: "//", go: "//", rust: "//", java: "//", c: "//", cpp: "//", csharp: "//", swift: "//", kt: "//", kotlin: "//", scala: "//", php: "//", sh: "#", bash: "#", shell: "#", zsh: "#", py: "#", python: "#", ruby: "#", rb: "#", yaml: "#", yml: "#", tf: "#", hcl: "#", toml: "#" };
    const c = (comments[(lang || "").toLowerCase()] || "#").replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const kw = new Set(("break case catch class const continue default do else enum export extends finally for from function if import in instanceof interface let new of package private protected public return static super switch this throw try typeof var void while with yield async await def fn impl pub struct self mut ref match lambda pass raise global assert del elif except nonlocal as is and or not None True False print println int str bool float char double long short unsigned void null true false").split(" "));
    const re = new RegExp(
      "(" + c + "[^\\n]*)" +
      "|('(?:[^'\\\\\\n]|\\\\.)*'|\"(?:[^\"\\\\\\n]|\\\\.)*\"|`(?:[^`\\\\]|\\\\.)*`)" +
      "|\\b(0x[0-9a-fA-F]+|\\d+(?:\\.\\d+)?)\\b" +
      "|\\b([A-Za-z_$][\\w$]*)\\b",
      "g"
    );
    let out = "", last = 0;
    for (let m; (m = re.exec(src));) {
      out += esc(src.slice(last, m.index));
      if (m[1]) out += `<span class="tc">${esc(m[1])}</span>`;
      else if (m[2]) out += `<span class="ts">${esc(m[2])}</span>`;
      else if (m[3]) out += `<span class="tn">${esc(m[3])}</span>`;
      else out += kw.has(m[4]) ? `<span class="tk">${esc(m[4])}</span>` : esc(m[4]);
      last = m.index + m[0].length;
    }
    return out + esc(src.slice(last));
  }

  function codeBlockHTML(lang, code) {
    const bid = "cb" + ++codeSeq;
    codeBlocks.set(bid, code);
    return (
      `<div class="codeblock"><div class="codehead"><span>${esc(lang || "code")}</span>` +
      `<button type="button" class="codecopy" data-bid="${bid}" title="Copy code">Copy</button></div>` +
      `<pre><code class="${lang ? "lang-" + esc(lang) : ""}">${highlight(code, lang)}</code></pre></div>`
    );
  }

  function md(src) {
    const fences = [];
    const park = (html) => {
      fences.push(html);
      return `\u0000${fences.length - 1}\u0000`;
    };
    let t = String(src ?? "").replace(/```(\w*)[^\S\n]*\n([\s\S]*?)```/g, (_, lang, code) =>
      park(codeBlockHTML(lang, code.replace(/\n$/, "")))
    );
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
      return park(html + `</tbody></table>`);
    });
    t = esc(t);
    t = t.replace(/^### (.+)$/gm, "<h3>$1</h3>");
    t = t.replace(/^## (.+)$/gm, "<h2>$1</h2>");
    t = t.replace(/^# (.+)$/gm, "<h1>$1</h1>");
    t = t.replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener">$1</a>');
    t = t.replace(/\*\*(.+?)\*\*/g, "<strong>$1</strong>");
    t = t.replace(/`([^`]+)`/g, "<code>$1</code>");
    t = t.replace(/^\d+\. (.+)$/gm, "<oli>$1</oli>");
    t = t.replace(/(<oli>.*<\/oli>\n?)+/g, (m) => "<ol>" + m.replace(/<\/?oli>/g, (x) => (x === "<oli>" ? "<li>" : "</li>")) + "</ol>");
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
  function outputHTML(e) {
    const lang = langOf(e.name || "", e.summary || "");
    if (lang && e.output.length < 200_000) return highlight(e.output, lang);
    return esc(e.output);
  }

  const CODE_EXT = /\.(py|go|js|mjs|ts|tsx|jsx|rs|java|c|h|cpp|hpp|cs|rb|php|sh|bash|sql|yaml|yml|json|toml|html|css|scss|lua|swift|kt|kts|scala)(\W|$)/;

  function langOf(name, summary) {
    const m = CODE_EXT.exec(summary || "");
    if (m) return m[1] === "mjs" ? "js" : m[1];
    if (name === "bash" || name === "shell" || name === "run") return "sh";
    return "";
  }

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
      const raw = (e.text || "").trim();
      if (!raw && !e.done) {
        n.innerHTML = `<span class="lbl">${esc(lastFlavor || "Thinking")}</span>`;
      } else {
        const lines = raw.split("\n");
        n.innerHTML = `<span class="lbl">${e.done ? "Thought" : "Thinking"}</span>${esc(e.done ? (lines[0] || "").slice(0, 160) : lines.slice(-2).join(" "))}`;
      }
    } else if (e.kind === "tool") {
      n.className = "turn";
      const st = e.denied ? "Declined" : e.error ? "Failed" : e.done ? "Done" : e.running ? "Running" : "Queued";
      const stCls = "st-" + st.toLowerCase();
      const open = openTools.has(e.id);
      let html = `<div class="tool ${stCls}"><div class="head"><span class="st">${st}</span><b>${esc(e.name || "")}</b><span class="sum">${esc(e.summary || "")}${e.dur ? " · " + esc(e.dur) : ""}</span></div>`;
      if (open && e.preview) html += `<pre class="diff">${diffHTML(e.preview)}</pre>`;
      if (open && e.output) html += `<pre class="out">${outputHTML(e)}</pre>`;
      html += `</div>`;
      n.innerHTML = html;
      n.querySelector(".head").onclick = () => {
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
      n.innerHTML = `<div class="k">${esc(e.name || "")}<button type="button" class="copy-btn sheet-copy" data-copy="${e.id}" title="Copy output">Copy</button></div><pre class="sheetbody">${esc(e.text || "")}</pre>`;
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
    setModeChip(h.mode);
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
    document.getElementById("send").title = running ? "Queue behind the running turn" : "Send";
    document.getElementById("stop").hidden = !running;
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
    chip.textContent = effort ? "effort " + effort : "effort";
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

  async function openModelMenu() {
    switchMode = "model";
    try {
      modelGroups = (await (await fetch("/api/models")).json()).groups || [];
    } catch (_) {
      modelGroups = [];
    }
    switchQ.value = "";
    switchQ.placeholder = "Switch model…";
    renderSwitchMenu("");
    switchMenu.hidden = false;
    switchQ.focus();
  }

  function openEffortMenu() {
    switchMode = "effort";
    switchQ.value = "";
    switchQ.placeholder = "Set effort…";
    renderSwitchMenu("");
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
          `<li class="agent ${a.done ? "done" : "running"}" data-agent="${esc(a.id)}" title="Open transcript"><span class="a-id">${esc(a.id)}</span> ${esc(a.desc || "")}` +
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
      document.getElementById("cost-line").textContent = ws.cost ? "Spend " + ws.cost : "";
      document.getElementById("mode-yolo").classList.toggle("on", !!ws.yolo);
      document.getElementById("mode-auto").classList.toggle("on", !!ws.auto);
      document.getElementById("mode-edits").classList.toggle("on", !!ws.edits);
      document.getElementById("mode-plan").classList.toggle("on", !!ws.plan);
      document.getElementById("mode-ro").classList.toggle("on", !!ws.readonly);
      setModeChip(ws.mode);
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
      curEl.innerHTML =
        `<div class="cur-row" title="The conversation open now">` +
        `<span class="cur-dot"></span><span class="cur-label">${esc(live.prefix || "")}${esc(live.label)}</span>` +
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
  const shell = document.querySelector(".shell");
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
    if (currentView === "trajectory") refreshTrajectory();
  });

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
  function setCollapsed(cls, on) {
    shell.classList.toggle(cls, on);
    localStorage.setItem("janetcode-" + cls, on ? "1" : "");
  }
  navToggle.addEventListener("click", () => setCollapsed("nav-collapsed", !shell.classList.contains("nav-collapsed")));
  workToggle.addEventListener("click", () => setCollapsed("work-collapsed", !shell.classList.contains("work-collapsed")));
  // The rails are drawers: closed by default so the conversation owns
  // the screen, and an explicit open/close persists across visits.
  if (localStorage.getItem("janetcode-nav-collapsed") !== "") shell.classList.add("nav-collapsed");
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
    const codecopy = ev.target.closest(".codecopy");
    if (codecopy) {
      const src = codeBlocks.get(codecopy.dataset.bid);
      if (src != null) {
        try {
          await navigator.clipboard.writeText(src);
          codecopy.textContent = "Copied";
          setTimeout(() => { codecopy.textContent = "Copy"; }, 1500);
        } catch (_) {}
      }
      return;
    }
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
