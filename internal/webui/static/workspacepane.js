// The workspace pane (Changes, Jobs, Subagents, Scripts, Plan tabs), the
// context ring/cost line, and the detail viewer. refreshWorkspace is the one
// fetch of /api/workspace: it paints this pane, stores the payload in
// S.lastWorkspace, and hands the header/mode/turn-file pieces back to
// app.js through the callbacks. Reads S.latest only to know whether the
// user has said anything yet (the context ring stays hidden before that).
(function (global) {
  "use strict";
  global.JanetWorkspacePane = function ({ S, esc, diffHTML, post, setModeChip, paintGit, paintTurnFiles }) {
    let selectedAgent = "";
    // A small count on a workbench tab: how many jobs run, how many
    // subagents are live, how many files changed.
    function tabCount(pane, n) {
      const el = document.getElementById("tab-n-" + pane);
      if (!el) return;
      el.hidden = !n;
      el.textContent = n ? String(n) : "";
    }
    // The Changes tab: one row per change with an M/A/D/U badge, the path
    // with its name emphasised, and a unified diff that opens in place.
    const openFileRows = new Set();
    const diffCache = new Map();
    function fileBadge(f) {
      if (!f.add && !f.del) return "U";
      if (/^(write_file|write|create_file)$/.test(f.tool || "") && !f.del) return "A";
      if (f.del && !f.add) return "D";
      return "M";
    }
    function paintFiles(files) {
      const el = document.getElementById("files");
      const sum = document.getElementById("files-sum");
      tabCount("files", files.length);
      if (!files.length) {
        el.innerHTML = `<li class="quiet">No files changed</li>`;
        sum.hidden = true;
        return;
      }
      let add = 0, del = 0;
      for (const f of files) { add += f.add || 0; del += f.del || 0; }
      sum.hidden = false;
      sum.innerHTML = `${files.length} change${files.length === 1 ? "" : "s"} <span class="add">+${add}</span> <span class="del">−${del}</span>`;
      el.innerHTML = files.map((f) => {
        const slash = f.path.lastIndexOf("/");
        const dir = slash >= 0 ? f.path.slice(0, slash + 1) : "";
        const open = openFileRows.has(f.seq);
        const b = fileBadge(f);
        return `<li class="frow${open ? " open" : ""}" data-seq="${f.seq}"><div class="frow-h" title="${esc(f.path)}"><span class="fbadge b-${b}">${b}</span>` +
          `<span class="fpath"><span class="fdir">${esc(dir)}</span><b>${esc(f.path.slice(slash + 1))}</b></span>` +
          `<span class="fstat"><span class="add">+${f.add || 0}</span> <span class="del">−${f.del || 0}</span></span></div>` +
          (open ? `<pre class="diff fdiff">${diffCache.has(f.seq) ? diffHTML(diffCache.get(f.seq)) : "Loading…"}</pre>` : "") + `</li>`;
      }).join("");
    }
    async function refreshWorkspace() {
      try {
        const ws = await (await fetch("/api/workspace")).json();
        const todos = document.getElementById("todos");
        if (!ws.todos || !ws.todos.length) todos.innerHTML = `<li class="quiet">No plan yet. When the model breaks a task into steps, they show here.</li>`;
        else todos.innerHTML = ws.todos.map((t) => {
          const cls = t.status === "completed" || t.status === "done" ? "done" : t.status === "in_progress" ? "doing" : "";
          return `<li class="${cls}">${esc(t.text)}</li>`;
        }).join("");
        paintFiles(ws.files || []);
        document.getElementById("undo-btn").hidden = !ws.can_undo;
        tabCount("jobs", (ws.jobs || []).length);
        tabCount("agents", (ws.agents || []).filter((a) => !a.done).length);
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
        const talked = [...S.latest.values()].some((e) => e.kind === "user");
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
        document.getElementById("mode-away").classList.toggle("on", !!ws.away);
        document.getElementById("mode-auto").classList.toggle("on", !!ws.auto);
        document.getElementById("mode-edits").classList.toggle("on", !!ws.edits);
        document.getElementById("mode-plan").classList.toggle("on", !!ws.plan);
        document.getElementById("mode-ro").classList.toggle("on", !!ws.readonly);
        document.getElementById("mode-dry").classList.toggle("on", !!ws.dry_run);
        setModeChip(ws.mode, ws.dry_run);
        S.lastWorkspace = ws;
        paintGit(ws);
        paintTurnFiles(ws.files);
      } catch (_) {}
    }

    document.getElementById("files").addEventListener("click", async (ev) => {
      const li = ev.target.closest("li[data-seq]");
      if (!li || ev.target.closest(".fdiff")) return;
      const seq = Number(li.dataset.seq);
      if (openFileRows.has(seq)) openFileRows.delete(seq); else openFileRows.add(seq);
      if (openFileRows.has(seq) && !diffCache.has(seq)) {
        try {
          const data = await (await fetch("/api/change?n=" + seq)).json();
          diffCache.set(seq, data.diff || "(no diff)");
        } catch (_) {
          openFileRows.delete(seq);
        }
      }
      if (S.lastWorkspace) paintFiles(S.lastWorkspace.files || []);
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
    return { refreshWorkspace, showDetail };
  };
})(window);
