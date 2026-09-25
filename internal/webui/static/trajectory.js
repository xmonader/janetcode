// The trajectory view: the session's folded ledger with a span chart
// over it, the record inspector and the brush and zoom. app.js creates it
// once and tells it when the turn's status changes; it fetches and draws
// everything else itself.
(function (global) {
  "use strict";
  const { esc } = global.JanetRender;
  const fmtTok = global.JanetFmt.tokens;
  const fmtDur = global.JanetFmt.duration;

  // isOpen reports whether the trajectory tab is the one on screen.
  global.JanetTrajectory = function ({ isOpen }) {
    const trajLedger = document.getElementById("traj-ledger");
    const trajChart = document.getElementById("traj-chart");
    const trajLanes = document.getElementById("traj-lanes");
    const trajTrack = document.getElementById("traj-track");
    const trajSel = document.getElementById("traj-sel");
    const trajSearch = document.getElementById("traj-search");
    const trajMeta = document.getElementById("traj-meta");
    const trajInspect = document.getElementById("traj-inspect");

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

    // Content is the row without the gutter: what it did, and for a tool
    // what came back — the same words the terminal ledger prints.
    function trajContent(r) {
      const parts = [];
      if (r.text) parts.push(r.text);
      if (r.preview) parts.push(r.preview);
      let body = parts.join(" · ");
      // A model call that left the session's own model says so up front,
      // the same words the terminal ledger prints (↪ cheap · …).
      if (r.kind === "message" && r.route) body = "↪ " + r.slot + " · " + body;
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
        const hay = [r.text, r.preview, r.result, r.tool, r.intent, r.model, r.slot, r.route, r.route_why, r.kind, "turn " + r.turn, "step " + r.step]
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
        .concat([`<span class="lg lg-mode">${esc(traj.mode.charAt(0).toUpperCase() + traj.mode.slice(1))}${traj.mode === "sequence" ? " · every record the same width" : " · " + esc(fmtDur(full))}</span>`])
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
                  `<span class="traj-preview">${esc(trajContent(r))}</span>` +
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
        if (d.route) facts.push("↪ " + d.slot + (d.route_why ? " — " + d.route_why : ""));
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
      if (!isOpen()) return;
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

    // The view refetches when the turn's shape changes — running flips,
    // the phase moves — not on every status tick, so a long thinking
    // pause no longer re-renders the panel twice a second. It also
    // refetches when it is opened.
    function statusChanged(st) {
      if (isOpen() && (st.running !== traj.lastRunning || st.phase !== traj.lastPhase)) {
        refreshTrajectory();
      }
      traj.lastRunning = !!st.running;
      traj.lastPhase = st.phase || "";
    }

    return { refresh: refreshTrajectory, statusChanged };
  };
})(window);
