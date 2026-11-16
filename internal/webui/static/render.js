// Pure renderers the page builds its HTML from: escaping, diffs with the
// changed span marked, the /perf and /usage cards, and link cards. None
// of them reads or writes the page; app.js draws what they return.
(function (global) {
  "use strict";

  // Inline SVG from the sprite in index.html — the only icon source.
  const icon = (name) => `<svg class="i" aria-hidden="true"><use href="#i-${name}"/></svg>`;

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

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
      out.push(`<span class="${cls}"><span class="ln">${esc(gutter)} │ </span><span class="mk">${kind === "+" || kind === "-" ? kind : " "}</span>${body}</span>`);
      if (kind !== "-") prevEnd = newN;
    }
    return out.join("\n");
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
      .map((s) => `<span class="${sliceCls[s.name] || "sl-harness"}" style="flex:${Math.max(s.ms, 1)}" title="${esc(s.name)} ${esc(JanetFmt.duration(s.ms))}"></span>`)
      .join("");
    const rows = (d.slices || [])
      .map((s) => {
        const bits = [];
        if (s.count) bits.push(`${s.count}×`);
        bits.push(`${s.pct}% of the session`);
        if (s.slowest_ms) bits.push(`slowest ${JanetFmt.duration(s.slowest_ms)}`);
        return `<div class="crow"><span class="cbar ${sliceCls[s.name] || "sl-harness"}"></span>` +
          `<b>${esc(s.name)}</b><span class="cms">${esc(JanetFmt.duration(s.ms))}</span>` +
          `<span class="cnote">${esc(bits.join(" · "))}${s.note ? " — " + esc(s.note) : ""}</span></div>`;
      })
      .join("");
    return (
      `<div class="k">where the wall clock went</div>` +
      `<div class="perfbar">${bar || `<span class="sl-harness" style="flex:1"></span>`}</div>` +
      `<div class="perfmeta">open ${esc(JanetFmt.duration(d.open_ms))} · in turns ${esc(JanetFmt.duration(d.turn_ms))} (${Math.round((d.turn_ms / total) * 100)}%) · outside them ${esc(JanetFmt.duration(d.idle_ms))}</div>` +
      rows
    );
  }

  function usageCardHTML(d) {
    if (!d) return "";
    const maxTok = Math.max(1, ...(d.models || []).map((m) => m.in + m.cache_read + m.cache_write + m.out));
    const rows = (d.models || [])
      .map((m) => {
        const all = m.in + m.cache_read + m.cache_write + m.out;
        // The share bar is its own full-width line under the row: sized
        // inline inside the flex row it took the row's whole width and
        // pushed the model name over the figures.
        return `<div class="crow urow"><b>${esc(m.model)}</b><span class="cms">${m.calls} call${m.calls === 1 ? "" : "s"}</span>` +
          `<span class="cnote">↑${JanetFmt.tokens(m.in)} (⧉${JanetFmt.tokens(m.cache_read + m.cache_write)}) ↓${JanetFmt.tokens(m.out)}` +
          (m.cost_usd ? ` · $${m.cost_usd.toFixed(4)}` : "") + `</span>` +
          `<span class="ubar"><span class="sl-model" style="width:${Math.max((all / maxTok) * 100, 2)}%"></span></span></div>`;
      })
      .join("");
    const deleg = (d.delegated || [])
      .map((x) => `<div class="crow urow"><b>delegated: ${esc(x.agent)}</b><span class="cms">${x.runs} run${x.runs === 1 ? "" : "s"}</span>` +
        `<span class="cnote">↑${JanetFmt.tokens(x.in)} ↓${JanetFmt.tokens(x.out)} · ${x.has_cost ? "$" + x.cost_usd.toFixed(4) : "cost unknown"}</span></div>`)
      .join("");
    const foot = [];
    if (d.in + d.out) foot.push(`↑${JanetFmt.tokens(d.in)} ↓${JanetFmt.tokens(d.out)}${d.cache ? " · ⧉" + JanetFmt.tokens(d.cache) : ""}`);
    if (d.cache_pct) foot.push(`cache hit ${d.cache_pct}%`);
    if (d.cost_usd) foot.push(`$${d.cost_usd.toFixed(4)} reported`);
    else foot.push("unpriced");
    if (d.ceiling) foot.push(`ceiling $${d.ceiling.toFixed(2)}`);
    return `<div class="k">what the session spent</div>` + rows + deleg + `<div class="perfmeta">${esc(foot.join(" · "))}</div>`;
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

  global.JanetRender = { icon, esc, changedSpan, markSpan, diffHTML, perfCardHTML, usageCardHTML, linkCards };
})(window);
